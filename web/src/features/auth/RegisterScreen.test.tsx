import type { AnchorHTMLAttributes } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { RegisterScreen } from './RegisterScreen';

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

describe('RegisterScreen', () => {
	beforeEach(() => {
		navigate.mockReset();
	});

	test('shows a mismatch error and makes no request', async () => {
		let calls = 0;
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/register`, () => {
				calls += 1;
				return HttpResponse.json(
					{ id: 'u1', email: 'alice@example.com' },
					{ status: 201 },
				);
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<RegisterScreen />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple!',
		);
		await user.click(screen.getByRole('button', { name: 'Create account' }));

		expect(
			await screen.findByText('Passwords do not match'),
		).toBeInTheDocument();
		expect(calls).toBe(0);
	});

	test('shows the check your email panel after a successful register', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/register`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(
					{ id: 'u1', email: 'alice@example.com' },
					{ status: 201 },
				);
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<RegisterScreen />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Create account' }));

		expect(await screen.findByText('Check your email')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Resend verification email' }),
		).toBeInTheDocument();
		expect(bodies).toEqual([
			{
				email: 'alice@example.com',
				password: 'correct horse battery staple',
			},
		]);
	});

	test('maps email_taken to a specific message', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/register`, () =>
				problem('email_taken', 409, 'Email already registered'),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<RegisterScreen />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Create account' }));

		expect(
			await screen.findByText('Email already registered'),
		).toBeInTheDocument();
	});
});
