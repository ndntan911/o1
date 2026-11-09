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

	async function signInForMFA(redirectTo?: string) {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				HttpResponse.json({
					mfa_required: true,
					challenge_token: 'c1',
				}),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen redirectTo={redirectTo} />);

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));

		await screen.findByLabelText('Authentication code');
		return user;
	}

	test('switches to the authenticator step without navigating when MFA is required', async () => {
		await signInForMFA('/w/acme/monitors');

		const input = screen.getByLabelText('Authentication code');
		expect(input).toHaveAttribute('inputmode', 'numeric');
		expect(input).toHaveAttribute('autocomplete', 'one-time-code');
		expect(input).toHaveAttribute('maxlength', '6');
		expect(
			screen.getByRole('button', { name: 'Use a recovery code' }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Back to sign in' }),
		).toBeInTheDocument();
		expect(navigate).not.toHaveBeenCalled();
		expect(window.location.href).not.toContain('c1');
	});

	test('submits the six-digit code with the challenge and navigates on success', async () => {
		const bodies: unknown[] = [];
		const user = await signInForMFA('/w/acme/monitors');
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login/totp`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);

		await user.type(screen.getByLabelText('Authentication code'), '123456');
		await user.click(screen.getByRole('button', { name: 'Verify' }));

		await waitFor(() =>
			expect(navigate).toHaveBeenCalledWith({ to: '/w/acme/monitors' }),
		);
		expect(bodies).toEqual([{ challenge_token: 'c1', code: '123456' }]);
	});

	test('clears cached non-me queries after completing the second factor', async () => {
		const queryClient = createTestQueryClient();
		const staleMonitorsKey = queryKeys.monitors('w1');
		queryClient.setQueryData(staleMonitorsKey, {
			pages: [],
			pageParams: [],
		});
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				HttpResponse.json({
					mfa_required: true,
					challenge_token: 'c1',
				}),
			),
			http.post(`${ORIGIN}/api/v1/auth/login/totp`, () =>
				HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				}),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<LoginScreen redirectTo="/" />, { queryClient });

		await user.type(screen.getByLabelText('Email'), 'alice@example.com');
		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Sign in' }));
		await user.type(
			await screen.findByLabelText('Authentication code'),
			'123456',
		);
		await user.click(screen.getByRole('button', { name: 'Verify' }));

		await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }));
		expect(queryClient.getQueryData(staleMonitorsKey)).toBeUndefined();
	});

	test('toggles to the recovery-code input and submits a normalized recovery code', async () => {
		const bodies: unknown[] = [];
		const user = await signInForMFA('/');
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login/totp`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);

		await user.click(
			screen.getByRole('button', { name: 'Use a recovery code' }),
		);

		const recovery = screen.getByLabelText('Recovery code');
		expect(recovery).toHaveAttribute('autocomplete', 'off');
		expect(
			screen.getByRole('button', { name: 'Use an authenticator code' }),
		).toBeInTheDocument();

		await user.type(recovery, ' abcde-fghjk ');
		await user.click(screen.getByRole('button', { name: 'Verify' }));

		await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }));
		expect(bodies).toEqual([
			{ challenge_token: 'c1', recovery_code: 'ABCDEFGHJK' },
		]);
	});

	test('renders a problem and keeps the second step for a wrong code', async () => {
		const user = await signInForMFA('/');
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login/totp`, () =>
				problem('invalid_totp', 401, 'Invalid authentication code'),
			),
		);

		await user.type(screen.getByLabelText('Authentication code'), '000000');
		await user.click(screen.getByRole('button', { name: 'Verify' }));

		expect(
			await screen.findByText('Invalid authentication code'),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Authentication code')).toBeInTheDocument();
		expect(navigate).not.toHaveBeenCalled();
	});

	test('disables the step links while a code is verifying', async () => {
		const user = await signInForMFA('/');
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login/totp`, async () => {
				await gate;
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);

		await user.type(screen.getByLabelText('Authentication code'), '123456');
		await user.click(screen.getByRole('button', { name: 'Verify' }));

		await waitFor(() =>
			expect(
				screen.getByRole('button', { name: 'Use a recovery code' }),
			).toBeDisabled(),
		);
		expect(
			screen.getByRole('button', { name: 'Back to sign in' }),
		).toBeDisabled();

		release();
		await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }));
	});

	test('returns to the password step from back to sign in', async () => {
		const user = await signInForMFA('/');

		await user.click(screen.getByRole('button', { name: 'Back to sign in' }));

		expect(screen.getByLabelText('Email')).toBeInTheDocument();
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
		expect(
			screen.queryByLabelText('Authentication code'),
		).not.toBeInTheDocument();
		expect(navigate).not.toHaveBeenCalled();
	});
});
