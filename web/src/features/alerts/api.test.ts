import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { putEmailAlertChannel } from './api';

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
