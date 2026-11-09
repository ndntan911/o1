import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { isApiProblem } from '@/lib/api/client';
import type { APIKey } from '@/lib/api/types';
import { createAPIKey, listAPIKeys, revokeAPIKey } from './api';

const ORIGIN = 'http://localhost:5173';
const keysPath = `${ORIGIN}/api/v1/workspaces/w1/api-keys`;

const key: APIKey = {
	id: 'k1',
	name: 'CI deploy',
	prefix: 'o1_abcdefghijkl',
	role: 'member',
	created_by: 'u1',
	created_at: '2026-11-09T09:00:00Z',
	last_used_at: null,
};

describe('API key API', () => {
	test('listAPIKeys gets the workspace keys', async () => {
		server.use(http.get(keysPath, () => HttpResponse.json({ keys: [key] })));

		await expect(listAPIKeys('w1')).resolves.toEqual({ keys: [key] });
	});

	test('createAPIKey posts the name and role and resolves the secret', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(keysPath, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(
					{ key, secret: 'o1_abcdefghijkl_secretonetime' },
					{ status: 201 },
				);
			}),
		);

		const created = await createAPIKey('w1', {
			name: 'CI deploy',
			role: 'member',
		});

		expect(bodies).toEqual([{ name: 'CI deploy', role: 'member' }]);
		expect(created).toEqual({
			key,
			secret: 'o1_abcdefghijkl_secretonetime',
		});
	});

	test('revokeAPIKey deletes the key and accepts 204', async () => {
		const deletedPaths: string[] = [];
		server.use(
			http.delete(`${keysPath}/k1`, ({ request }) => {
				deletedPaths.push(new URL(request.url).pathname);
				return new HttpResponse(null, { status: 204 });
			}),
		);

		await expect(revokeAPIKey('w1', 'k1')).resolves.toBeUndefined();
		expect(deletedPaths).toEqual(['/api/v1/workspaces/w1/api-keys/k1']);
	});

	test('a duplicate name surfaces api_key_name_taken', async () => {
		server.use(
			http.post(keysPath, () =>
				HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Conflict',
						status: 409,
						code: 'api_key_name_taken',
						detail: 'an active key with this name already exists',
					},
					{ status: 409 },
				),
			),
		);

		try {
			await createAPIKey('w1', { name: 'CI deploy', role: 'member' });
			throw new Error('expected createAPIKey to reject');
		} catch (error) {
			expect(isApiProblem(error)).toBe(true);
			if (isApiProblem(error)) {
				expect(error.problem.code).toBe('api_key_name_taken');
				expect(error.problem.detail).toBe(
					'an active key with this name already exists',
				);
			}
		}
	});
});
