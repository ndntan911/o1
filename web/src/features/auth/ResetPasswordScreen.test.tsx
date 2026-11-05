import type { AnchorHTMLAttributes } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { ResetPasswordScreen } from './ResetPasswordScreen';

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

describe('ResetPasswordScreen', () => {
	beforeEach(() => {
		navigate.mockReset();
	});

	test('shows a mismatch error and makes no request', async () => {
		let calls = 0;
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/password/reset`, () => {
				calls += 1;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<ResetPasswordScreen token="reset-token" />);

		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple!',
		);
		await user.click(screen.getByRole('button', { name: 'Reset password' }));

		expect(
			await screen.findByText('Passwords do not match'),
		).toBeInTheDocument();
		expect(calls).toBe(0);
	});

	test('posts the new password and shows the success copy', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/password/reset`, async ({ request }) => {
				bodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();
		renderWithProviders(<ResetPasswordScreen token="reset-token" />);

		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Reset password' }));

		expect(
			await screen.findByText('Your password has been reset.'),
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Sign in' })).toBeInTheDocument();
		expect(bodies).toEqual([
			{ token: 'reset-token', new_password: 'correct horse battery staple' },
		]);
	});

	test('shows a recovery link when the reset token is invalid', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/password/reset`, () =>
				problem('token_invalid', 400, 'Invalid or expired reset token'),
			),
		);
		const user = userEvent.setup();
		renderWithProviders(<ResetPasswordScreen token="used-token" />);

		await user.type(
			screen.getByLabelText('Password'),
			'correct horse battery staple',
		);
		await user.type(
			screen.getByLabelText('Confirm password'),
			'correct horse battery staple',
		);
		await user.click(screen.getByRole('button', { name: 'Reset password' }));

		expect(
			await screen.findByText('This reset link is invalid or expired'),
		).toBeInTheDocument();
		expect(
			screen.getByRole('link', { name: 'Request a new reset link' }),
		).toBeInTheDocument();
	});
});
