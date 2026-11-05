import type { AnchorHTMLAttributes } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { ForgotPasswordScreen } from './ForgotPasswordScreen';

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

describe('ForgotPasswordScreen', () => {
	beforeEach(() => {
		navigate.mockReset();
	});

	test.each(['alice@example.com', 'nobody@example.com'])(
		'shows the same neutral message for %s',
		async (email) => {
			const bodies: unknown[] = [];
			server.use(
				http.post(
					`${ORIGIN}/api/v1/auth/password/forgot`,
					async ({ request }) => {
						bodies.push(await request.json());
						return new HttpResponse(null, { status: 204 });
					},
				),
			);
			const user = userEvent.setup();
			renderWithProviders(<ForgotPasswordScreen />);

			await user.type(screen.getByLabelText('Email'), email);
			await user.click(screen.getByRole('button', { name: 'Send reset link' }));

			expect(
				await screen.findByText('If an account exists, we sent a reset link.'),
			).toBeInTheDocument();
			expect(bodies).toEqual([{ email }]);
			expect(
				screen.queryByText(/no account|not found/i),
			).not.toBeInTheDocument();
		},
	);
});
