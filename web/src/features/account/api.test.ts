import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { isApiProblem } from '@/lib/api/client';
import {
	disableTOTP,
	enableTOTP,
	getTOTPState,
	regenerateRecoveryCodes,
	setupTOTP,
} from './api';

const ORIGIN = 'http://localhost:5173';
const base = `${ORIGIN}/api/v1/auth/2fa`;

const setup = {
	secret: 'JBSWY3DPEHPK3PXP',
	otpauth_uri:
		'otpauth://totp/o1%20uptime:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=o1%20uptime',
};

const recoveryCodes = [
	'aaaaa-bbbbb',
	'ccccc-ddddd',
	'eeeee-fffff',
	'ggggg-hhhhh',
	'iiiii-jjjjj',
	'kkkkk-lllll',
	'mmmmm-nnnnn',
	'ooooo-ppppp',
	'qqqqq-rrrrr',
	'sssss-ttttt',
];

describe('account 2FA API', () => {
	test('getTOTPState gets /auth/2fa', async () => {
		const paths: string[] = [];
		server.use(
			http.get(base, ({ request }) => {
				paths.push(new URL(request.url).pathname);
				return HttpResponse.json({
					enabled: true,
					recovery_codes_remaining: 8,
				});
			}),
		);

		await expect(getTOTPState()).resolves.toEqual({
			enabled: true,
			recovery_codes_remaining: 8,
		});
		expect(paths).toEqual(['/api/v1/auth/2fa']);
	});

	test('setupTOTP posts the password and returns the secret and URI', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/setup`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(setup);
			}),
		);

		await expect(
			setupTOTP({ password: 'correct horse battery' }),
		).resolves.toEqual(setup);
		expect(bodies).toEqual([{ password: 'correct horse battery' }]);
	});

	test('enableTOTP posts the password and code and returns recovery codes', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/enable`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({ recovery_codes: recoveryCodes });
			}),
		);

		await expect(
			enableTOTP({ password: 'correct horse battery', code: '123456' }),
		).resolves.toEqual({ recovery_codes: recoveryCodes });
		expect(bodies).toEqual([
			{ password: 'correct horse battery', code: '123456' },
		]);
	});

	test('disableTOTP posts a TOTP code and accepts 204', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/disable`, async ({ request }) => {
				bodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await expect(
			disableTOTP({ password: 'correct horse battery', code: '123456' }),
		).resolves.toBeUndefined();
		expect(bodies).toEqual([
			{ password: 'correct horse battery', code: '123456' },
		]);
	});

	test('disableTOTP posts a recovery code instead of a TOTP code', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/disable`, async ({ request }) => {
				bodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await disableTOTP({
			password: 'correct horse battery',
			recovery_code: 'aaaaa-bbbbb',
		});
		expect(bodies).toEqual([
			{ password: 'correct horse battery', recovery_code: 'aaaaa-bbbbb' },
		]);
	});

	test('regenerateRecoveryCodes posts the factor and returns new codes', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(`${base}/recovery-codes`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({ recovery_codes: recoveryCodes });
			}),
		);

		await expect(
			regenerateRecoveryCodes({
				password: 'correct horse battery',
				code: '123456',
			}),
		).resolves.toEqual({ recovery_codes: recoveryCodes });
		expect(bodies).toEqual([
			{ password: 'correct horse battery', code: '123456' },
		]);
	});

	test('a wrong password surfaces invalid_credentials', async () => {
		server.use(
			http.post(`${base}/setup`, () =>
				HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Invalid password',
						status: 401,
						code: 'invalid_credentials',
					},
					{ status: 401 },
				),
			),
		);

		try {
			await setupTOTP({ password: 'wrong' });
			throw new Error('expected setupTOTP to reject');
		} catch (error) {
			expect(isApiProblem(error)).toBe(true);
			if (isApiProblem(error)) {
				expect(error.problem.code).toBe('invalid_credentials');
			}
		}
	});
});
