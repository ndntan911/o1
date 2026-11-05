import { describe, expect, test } from 'vitest';
import { parseProblem } from './problems';

describe('parseProblem', () => {
	test('parses an RFC 9457 problem with fields', async () => {
		const response = new Response(
			JSON.stringify({
				type: 'about:blank',
				title: 'Validation Error',
				status: 400,
				code: 'validation_error',
				fields: { url: 'url is not allowed' },
			}),
			{ status: 400, headers: { 'Content-Type': 'application/problem+json' } },
		);
		await expect(parseProblem(response)).resolves.toMatchObject({
			code: 'validation_error',
			fields: { url: 'url is not allowed' },
		});
	});

	test('falls back to internal_error for a non-problem response', async () => {
		const response = new Response('boom', { status: 500 });
		await expect(parseProblem(response)).resolves.toMatchObject({
			status: 500,
			code: 'internal_error',
		});
	});
});
