import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { isApiProblem } from '@/lib/api/client';
import {
	createAlertChannel,
	deleteAlertChannel,
	putEmailAlertChannel,
	testAlertChannel,
	updateAlertChannel,
} from './api';

const ORIGIN = 'http://localhost:5173';
const emailChannelPath = `${ORIGIN}/api/v1/workspaces/w1/alert-channels/email`;

describe('putEmailAlertChannel', () => {
	test('sends the email settings and resolves without a value', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.put(emailChannelPath, async ({ request }) => {
				bodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);

		const result: void = await putEmailAlertChannel('w1', {
			recipients: ['ops@example.com'],
			enabled: true,
		});

		expect(result).toBeUndefined();
		expect(bodies).toEqual([
			{ recipients: ['ops@example.com'], enabled: true },
		]);
	});
});

const channelsPath = `${ORIGIN}/api/v1/workspaces/w1/alert-channels`;

describe('alert channel API', () => {
	test('createAlertChannel posts the channel body', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(channelsPath, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({ id: 'c2' }, { status: 201 });
			}),
		);

		await createAlertChannel('w1', {
			kind: 'webhook',
			name: 'Deploy hook',
			enabled: true,
			url: 'https://example.com/o1',
			signing_secret: '0123456789abcdef',
		});

		expect(bodies).toEqual([
			{
				kind: 'webhook',
				name: 'Deploy hook',
				enabled: true,
				url: 'https://example.com/o1',
				signing_secret: '0123456789abcdef',
			},
		]);
	});

	test('updateAlertChannel patches the channel', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.patch(`${channelsPath}/c2`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({ id: 'c2' });
			}),
		);

		await updateAlertChannel('w1', 'c2', { enabled: false });

		expect(bodies).toEqual([{ enabled: false }]);
	});

	test('deleteAlertChannel accepts 204', async () => {
		server.use(
			http.delete(
				`${channelsPath}/c2`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);

		await expect(deleteAlertChannel('w1', 'c2')).resolves.toBeUndefined();
	});

	test('testAlertChannel posts to the test endpoint and surfaces a 422', async () => {
		server.use(
			http.post(`${channelsPath}/c2/test`, () =>
				HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Test Send Failed',
						status: 422,
						code: 'test_send_failed',
						detail: 'webhook returned HTTP 500',
					},
					{ status: 422 },
				),
			),
		);

		try {
			await testAlertChannel('w1', 'c2');
			throw new Error('expected testAlertChannel to reject');
		} catch (error) {
			expect(isApiProblem(error)).toBe(true);
			if (isApiProblem(error)) {
				expect(error.problem.code).toBe('test_send_failed');
				expect(error.problem.detail).toBe('webhook returned HTTP 500');
			}
		}
	});
});
