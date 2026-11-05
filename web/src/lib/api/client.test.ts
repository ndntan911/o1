import { beforeEach, describe, expect, test, vi } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import {
	getWorkspaceSuspended,
	markWorkspaceSuspended,
	resetWorkspaceStatusForTests,
} from '@/lib/workspace-status';
import { apiRequest, setUnauthenticatedHandler } from './client';
import type { Me } from './types';

const ORIGIN = 'http://localhost:5173';
const problem = (body: object, status: number) =>
	HttpResponse.json(body, {
		status,
		headers: { 'Content-Type': 'application/problem+json' },
	});

describe('apiRequest', () => {
	beforeEach(() => {
		setUnauthenticatedHandler(null);
		resetWorkspaceStatusForTests();
	});

	test('returns JSON for a safe request', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () =>
				HttpResponse.json<Me>({
					user: { id: 'u1', email: 'a@example.com', is_admin: false },
					workspaces: [],
				}),
			),
		);
		await expect(apiRequest<Me>('/me')).resolves.toMatchObject({
			user: { email: 'a@example.com' },
		});
	});

	test('throws ApiError with the problem code and fields', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/workspaces`, () =>
				problem(
					{
						type: 'about:blank',
						title: 'Validation Error',
						status: 400,
						code: 'validation_error',
						fields: { name: 'bad' },
					},
					400,
				),
			),
		);
		await expect(
			apiRequest('/workspaces', { method: 'POST', body: { name: '' } }),
		).rejects.toMatchObject({
			problem: { code: 'validation_error', fields: { name: 'bad' } },
		});
	});

	test('calls the handler for unauthenticated but not invalid_credentials', async () => {
		const handler = vi.fn();
		setUnauthenticatedHandler(handler);
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () =>
				problem(
					{
						type: 'about:blank',
						title: 'Authentication required',
						status: 401,
						code: 'unauthenticated',
					},
					401,
				),
			),
			http.post(`${ORIGIN}/api/v1/auth/login`, () =>
				problem(
					{
						type: 'about:blank',
						title: 'Invalid email or password',
						status: 401,
						code: 'invalid_credentials',
					},
					401,
				),
			),
		);

		await expect(apiRequest('/me')).rejects.toBeDefined();
		await expect(
			apiRequest('/auth/login', { method: 'POST', body: {} }),
		).rejects.toBeDefined();
		expect(handler).toHaveBeenCalledTimes(1);
	});

	test('resolves undefined for a 204 response', async () => {
		server.use(
			http.post(
				`${ORIGIN}/api/v1/workspaces`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		await expect(
			apiRequest('/workspaces', { method: 'POST', body: { name: 'acme' } }),
		).resolves.toBeUndefined();
	});

	test('appends defined query values only', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/monitors`, ({ request }) =>
				HttpResponse.json({ url: request.url }),
			),
		);
		const result = await apiRequest<{ url: string }>('/monitors', {
			query: { page: 2, suspended: true, cursor: null, empty: undefined },
		});
		const url = new URL(result.url);
		expect(url.searchParams.get('page')).toBe('2');
		expect(url.searchParams.get('suspended')).toBe('true');
		expect(url.searchParams.has('cursor')).toBe(false);
		expect(url.searchParams.has('empty')).toBe(false);
	});

	test('tracks workspace suspension until an unsafe request succeeds', async () => {
		server.use(
			http.post(`${ORIGIN}/api/v1/monitors`, () =>
				problem(
					{
						type: 'about:blank',
						title: 'Workspace suspended',
						status: 403,
						code: 'workspace_suspended',
					},
					403,
				),
			),
		);
		await expect(
			apiRequest('/monitors', { method: 'POST', body: {} }),
		).rejects.toBeDefined();
		expect(getWorkspaceSuspended()).toBe(true);

		server.use(
			http.post(
				`${ORIGIN}/api/v1/monitors`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		await apiRequest('/monitors', { method: 'POST', body: {} });
		expect(getWorkspaceSuspended()).toBe(false);
	});

	test('leaves the suspension flag alone for a successful GET', async () => {
		markWorkspaceSuspended();
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () =>
				HttpResponse.json<Me>({
					user: { id: 'u1', email: 'a@example.com', is_admin: false },
					workspaces: [],
				}),
			),
		);
		await apiRequest('/me');
		expect(getWorkspaceSuspended()).toBe(true);
	});
});
