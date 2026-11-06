import type { AnchorHTMLAttributes } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import {
	getWorkspaceSuspended,
	markWorkspaceSuspended,
	resetWorkspaceStatusForTests,
} from '@/lib/workspace-status';
import { LoginScreen } from './LoginScreen';

const ORIGIN = 'http://localhost:5173';

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock('@tanstack/react-router', async (importOriginal) => {
	const actual =
		await importOriginal<typeof import('@tanstack/react-router')>();
	return {
		...actual,
		useNavigate: () => navigate,
		Link: ({
			to,
			children,
			...props
		}: { to?: string } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
			<a href={to} {...props}>
				{children}
			</a>
		),
	};
});

function problem(code: string, status: number, detail: string) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title: 'Request failed',
			status,
			code,
			detail,
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

describe('LoginScreen', () => {
	beforeEach(() => {
		navigate.mockReset();
		resetWorkspaceStatusForTests();
	});

	test('shows a validation error and makes no request for a malformed email', async () => {
		let calls = 0;
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () => {
				calls += 1;
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen />);

		await user.type(screen.getByLabelText('Email'), 'not-an-email');
		await user.type(screen.getByLabelText('Password'), 'whatever');
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		expect(
			await screen.findByText('Enter a valid email address'),
		).toBeInTheDocument();
		expect(calls).toBe(0);
	});

	test('posts credentials and navigates to the redirect target', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen redirectTo="/w/acme/monitors" />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		await waitFor(() =>
			expect(navigate).toHaveBeenCalledWith({ to: '/w/acme/monitors' }),
		);
		expect(bodies).toEqual([
			{
				email: 'alice@example.com',
				password: 'correct horse battery staple',
			},
		]);
	});

	test('a successful login drops cached non-me queries and clears suspension', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				HttpResponse.json({
					user: { id: 'u2', email: 'bob@example.com' },
				}),
			),
		);
		const queryClient = createTestQueryClient();
		const staleMonitorsKey = queryKeys.monitors('w1');
		const meKey = queryKeys.me;
		const meData = {
			user: { id: 'u1', email: 'alice@example.com', is_admin: false },
			workspaces: [],
		};
		queryClient.setQueryData(staleMonitorsKey, {
			pages: [],
			pageParams: [],
		});
		queryClient.setQueryData(meKey, meData);
		markWorkspaceSuspended();
		const user = userEvent.setup();

		renderWithProviders(<LoginScreen redirectTo="/" />, { queryClient });

		await user.type(screen.getByLabelText('Email'), 'bob@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }));
		expect(queryClient.getQueryData(staleMonitorsKey)).toBeUndefined();
		expect(queryClient.getQueryData(meKey)).toEqual(meData);
		expect(getWorkspaceSuspended()).toBe(false);
	});

	test('maps invalid credentials to a specific message', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				problem('invalid_credentials', 401, 'Invalid email or password'),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		expect(
			await screen.findByText('Invalid email or password'),
		).toBeInTheDocument();
		expect(screen.queryByText('invalid_credentials')).not.toBeInTheDocument();
		expect(navigate).not.toHaveBeenCalled();
	});

	test('shows the verification state and resend control when email is not verified', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				problem('email_not_verified', 403, 'Email address is not verified'),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		expect(
			await screen.findByText('Email address is not verified'),
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Resend verification email' }),
		).toBeInTheDocument();
	});
});
