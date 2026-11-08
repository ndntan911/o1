import { describe, expect, test } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { getPublicStatus, getStatusPage, updateStatusPage } from './api';

const ORIGIN = 'http://localhost:5173';
const configPath = `${ORIGIN}/api/v1/workspaces/w1/status-page`;

describe('status page API', () => {
	test('getStatusPage reads the workspace config', async () => {
		server.use(
			http.get(configPath, () =>
				HttpResponse.json({
					enabled: false,
					title: 'Acme',
					description: '',
					monitor_ids: [],
				}),
			),
		);

		await expect(getStatusPage('w1')).resolves.toEqual({
			enabled: false,
			title: 'Acme',
			description: '',
			monitor_ids: [],
		});
	});

	test('updateStatusPage puts the exact config body', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.put(configPath, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({
					enabled: true,
					title: 'Acme status',
					description: 'Live',
					monitor_ids: ['m1'],
				});
			}),
		);

		await updateStatusPage('w1', {
			enabled: true,
			title: 'Acme status',
			description: 'Live',
			monitor_ids: ['m1'],
		});

		expect(bodies).toEqual([
			{
				enabled: true,
				title: 'Acme status',
				description: 'Live',
				monitor_ids: ['m1'],
			},
		]);
	});

	test('getPublicStatus reads the public endpoint', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/public/status/acme`, () =>
				HttpResponse.json({
					workspace: { name: 'Acme', slug: 'acme' },
					page: {
						title: 'Acme status',
						description: 'Live',
						updated_at: '2026-11-08T09:00:00Z',
					},
					monitors: [],
					incidents: { active: [], recent: [] },
				}),
			),
		);

		const page = await getPublicStatus('acme');
		expect(page.workspace.slug).toBe('acme');
		expect(page.monitors).toEqual([]);
	});
});
