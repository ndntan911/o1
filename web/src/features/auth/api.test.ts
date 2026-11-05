import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import {
	forgotPassword,
	login,
	logout,
	register,
	resendVerification,
	resetPassword,
	verifyEmail,
} from './api';

const ORIGIN = 'http://localhost:5173';

function problem(code: string, status: number) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title: 'Request failed',
			status,
			code,
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

describe('auth api', () => {
	test('register POSTs email and password', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/register`, async ({ request }) => {
				expect(request.method).toBe('POST');
				await expect(request.json()).resolves.toEqual({
					email: 'alice@example.com',
					password: 'correct horse battery staple',
				});
				return HttpResponse.json(
					{ id: 'u1', email: 'alice@example.com' },
					{ status: 201 },
				);
			}),
		);

		await expect(
			register({
				email: 'alice@example.com',
				password: 'correct horse battery staple',
			}),
		).resolves.toEqual({ id: 'u1', email: 'alice@example.com' });
	});

	test('verifyEmail POSTs the token and resolves on 204', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/verify-email`, async ({ request }) => {
				expect(request.method).toBe('POST');
				await expect(request.json()).resolves.toEqual({
					token: 'verify-token',
				});
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await expect(verifyEmail('verify-token')).resolves.toBeUndefined();
	});

	test('resendVerification POSTs the email', async () => {
		server.use(
			http.post(
				`${ORIGIN}/api/v1/auth/verify-email/resend`,
				async ({ request }) => {
					expect(request.method).toBe('POST');
					await expect(request.json()).resolves.toEqual({
						email: 'alice@example.com',
					});
					return new HttpResponse(null, { status: 204 });
				},
			),
		);

		await expect(
			resendVerification('alice@example.com'),
		).resolves.toBeUndefined();
	});

	test('login POSTs credentials and returns the user', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, async ({ request }) => {
				expect(request.method).toBe('POST');
				await expect(request.json()).resolves.toEqual({
					email: 'alice@example.com',
					password: 'correct horse battery staple',
				});
				return HttpResponse.json({
					user: { id: 'u1', email: 'alice@example.com' },
				});
			}),
		);

		await expect(
			login({
				email: 'alice@example.com',
				password: 'correct horse battery staple',
			}),
		).resolves.toEqual({
			user: { id: 'u1', email: 'alice@example.com' },
		});
	});

	test('login surfaces invalid_credentials as an ApiError', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				problem('invalid_credentials', 401),
			),
		);

		await expect(
			login({ email: 'alice@example.com', password: 'wrong password' }),
		).rejects.toMatchObject({
			problem: { code: 'invalid_credentials', status: 401 },
		});
	});

	test('logout POSTs without a body', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/logout`, async ({ request }) => {
				expect(request.method).toBe('POST');
				await expect(request.text()).resolves.toBe('');
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await expect(logout()).resolves.toBeUndefined();
	});

	test('forgotPassword POSTs the email', async () => {
		server.use(
			http.post(
				`${ORIGIN}/api/v1/auth/password/forgot`,
				async ({ request }) => {
					expect(request.method).toBe('POST');
					await expect(request.json()).resolves.toEqual({
						email: 'alice@example.com',
					});
					return new HttpResponse(null, { status: 204 });
				},
			),
		);

		await expect(forgotPassword('alice@example.com')).resolves.toBeUndefined();
	});

	test('resetPassword POSTs token and new_password', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/auth/password/reset`, async ({ request }) => {
				expect(request.method).toBe('POST');
				await expect(request.json()).resolves.toEqual({
					token: 'reset-token',
					new_password: 'correct horse battery staple',
				});
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await expect(
			resetPassword({
				token: 'reset-token',
				new_password: 'correct horse battery staple',
			}),
		).resolves.toBeUndefined();
	});
});
