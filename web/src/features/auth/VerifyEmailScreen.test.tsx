import type { AnchorHTMLAttributes } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import { VerifyEmailScreen } from './VerifyEmailScreen';

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

describe('VerifyEmailScreen', () => {
	beforeEach(() => {
		navigate.mockReset();
	});

	test('verifies the token once and shows the success state', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/verify-email`, async ({ request }) => {
				bodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);

		renderWithProviders(<VerifyEmailScreen token="verify-token" />);

		expect(await screen.findByText('Email verified')).toBeInTheDocument();
		expect(bodies).toEqual([{ token: 'verify-token' }]);
	});

	test('shows the expired link message for token_invalid', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/verify-email`, () =>
				problem('token_invalid', 400, 'Invalid or expired verification token'),
			),
		);

		renderWithProviders(<VerifyEmailScreen token="used-token" />);

		expect(
			await screen.findByText('This verification link is invalid or expired'),
		).toBeInTheDocument();
	});

	test('shows a missing token message without calling the API', async () => {
		let calls = 0;
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/verify-email`, () => {
				calls += 1;
				return new HttpResponse(null, { status: 204 });
			}),
		);

		renderWithProviders(<VerifyEmailScreen />);

		expect(
			await screen.findByText('This verification link is missing its token'),
		).toBeInTheDocument();
		expect(calls).toBe(0);
	});
});
