import { afterEach, describe, expect, test } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import {
	RouterProvider,
	createMemoryHistory,
	type RouterHistory,
} from '@tanstack/react-router';
import { HttpResponse, delay, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { apiRequest, setUnauthenticatedHandler } from '@/lib/api/client';
import { queryKeys } from '@/lib/query/keys';
import { registerUnauthenticatedHandler } from '@/lib/unauthenticated-handler';
import type { Me } from '@/lib/api/types';
import { createAppRouter } from './router';

const ORIGIN = 'http://localhost:5173';

const memberMe: Me = {
	user: { id: 'u1', email: 'alice@example.com', is_admin: false },
	workspaces: [
		{ id: 'w1', name: 'Acme Status', slug: 'acme-status', role: 'member' },
	],
};

const detailSummary = {
	window: '6h',
	uptime: 0.99,
	checks: 100,
	up: 99,
	down: 1,
	avg_latency_ms: 120,
	p95_latency_ms: 200,
	min_latency_ms: 80,
	max_latency_ms: 300,
};

const detailMonitor = {
	id: 'm1',
	workspace_id: 'w1',
	name: 'API',
	url: 'https://api.example.com/health',
	method: 'GET',
	headers: {},
	has_body: false,
	content_type: null,
	expected_status: '2xx',
	interval_seconds: 60,
	timeout_seconds: 10,
	confirmation_threshold: 1,
	recovery_threshold: 1,
	paused: false,
	status: 'up',
	last_check: null,
	summary_24h: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

function unauthenticatedProblem() {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title: 'Authentication required',
			status: 401,
			code: 'unauthenticated',
		},
		{
			status: 401,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

const me: Me = {
	user: { id: 'u1', email: 'alice@example.com', is_admin: false },
	workspaces: [],
};

function renderRouter(initialEntry: string) {
	const queryClient = createTestQueryClient();
	const history: RouterHistory = createMemoryHistory({
		initialEntries: [initialEntry],
	});
	const router = createAppRouter({ history, queryClient });
	registerUnauthenticatedHandler(router, queryClient);

	render(
		<QueryClientProvider client={queryClient}>
			<RouterProvider router={router} />
		</QueryClientProvider>,
	);

	return { router, queryClient };
}

describe('router guards', () => {
	afterEach(() => {
		setUnauthenticatedHandler(null);
	});

	test('logged-out / redirects to /login and preserves the destination', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => unauthenticatedProblem()));

		const { router } = renderRouter('/');

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(router.state.location.searchStr).toBe('?redirect=%2F');
		expect(router.state.location.search).toEqual({ redirect: '/' });
	});

	test('logged-out users can open the login screen', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => unauthenticatedProblem()));

		const { router } = renderRouter('/login');

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(screen.getByLabelText('Email')).toBeInTheDocument();
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
	});

	test('authenticated users visiting /login are redirected home', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(me)));

		const { router } = renderRouter('/login');

		expect(
			await screen.findByRole('heading', {
				name: 'Workspaces',
			}),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/workspaces');
	});

	test('a cached me refetch 401 does not fail open back into the app', async () => {
		let authenticated = true;
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () =>
				authenticated ? HttpResponse.json(me) : unauthenticatedProblem(),
			),
		);

		const { router, queryClient } = renderRouter('/');
		expect(
			await screen.findByRole('heading', {
				name: 'Workspaces',
			}),
		).toBeInTheDocument();

		authenticated = false;
		await act(async () => {
			await queryClient.invalidateQueries({ queryKey: queryKeys.me });
		});

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole('heading', { name: 'Workspaces' }),
		).not.toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(router.state.location.searchStr).toBe('?redirect=%2Fworkspaces');
	});

	test('a non-me 401 while me is cached lands on login without the error boundary', async () => {
		let authenticated = true;
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, async () => {
				if (authenticated) return HttpResponse.json(me);
				await delay(25);
				return unauthenticatedProblem();
			}),
			http.get(`${ORIGIN}/api/v1/workspaces`, () => unauthenticatedProblem()),
		);

		const { router } = renderRouter('/');
		expect(
			await screen.findByRole('heading', {
				name: 'Workspaces',
			}),
		).toBeInTheDocument();

		authenticated = false;
		await act(async () => {
			await expect(apiRequest('/workspaces')).rejects.toMatchObject({
				problem: { code: 'unauthenticated' },
			});
		});

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(screen.queryByText('Something went wrong!')).not.toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(router.state.location.searchStr).toBe('?redirect=%2Fworkspaces');
	});

	test('a member can open the new monitor form', async () => {
		const memberMe: Me = {
			user: { id: 'u1', email: 'alice@example.com', is_admin: false },
			workspaces: [
				{
					id: 'w1',
					name: 'Acme Status',
					slug: 'acme-status',
					role: 'member',
				},
			],
		};
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(memberMe)),
		);

		renderRouter('/w/acme-status/monitors/new');

		expect(
			await screen.findByRole('heading', { name: 'New monitor' }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Create monitor' }),
		).toBeInTheDocument();
	});

	test('a viewer is redirected away from the new monitor form', async () => {
		const viewerMe: Me = {
			user: { id: 'u1', email: 'alice@example.com', is_admin: false },
			workspaces: [
				{
					id: 'w1',
					name: 'Acme Status',
					slug: 'acme-status',
					role: 'viewer',
				},
			],
		};
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(viewerMe)),
			http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors`, () =>
				HttpResponse.json({ monitors: [] }),
			),
		);

		const { router } = renderRouter('/w/acme-status/monitors/new');

		await waitFor(() =>
			expect(router.state.location.pathname).toBe('/w/acme-status/monitors'),
		);
		expect(
			screen.queryByRole('heading', { name: 'New monitor' }),
		).not.toBeInTheDocument();
	});

	test('a viewer is redirected away from the edit monitor form', async () => {
		const viewerMe: Me = {
			user: { id: 'u1', email: 'alice@example.com', is_admin: false },
			workspaces: [
				{
					id: 'w1',
					name: 'Acme Status',
					slug: 'acme-status',
					role: 'viewer',
				},
			],
		};
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(viewerMe)),
			http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors`, () =>
				HttpResponse.json({ monitors: [] }),
			),
		);

		const { router } = renderRouter('/w/acme-status/monitors/m1/edit');

		await waitFor(() =>
			expect(router.state.location.pathname).toBe('/w/acme-status/monitors'),
		);
		expect(
			screen.queryByRole('heading', { name: 'Edit monitor' }),
		).not.toBeInTheDocument();
	});

	test('the monitor detail route drives summary and results with the window search param', async () => {
		const summaryWindows: (string | null)[] = [];
		const resultsWindows: (string | null)[] = [];
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(memberMe)),
			http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors/m1`, () =>
				HttpResponse.json(detailMonitor),
			),
			http.get(
				`${ORIGIN}/api/v1/workspaces/w1/monitors/m1/summary`,
				({ request }) => {
					summaryWindows.push(new URL(request.url).searchParams.get('window'));
					return HttpResponse.json(detailSummary);
				},
			),
			http.get(
				`${ORIGIN}/api/v1/workspaces/w1/monitors/m1/results`,
				({ request }) => {
					resultsWindows.push(new URL(request.url).searchParams.get('window'));
					return HttpResponse.json({ results: [] });
				},
			),
		);

		const { router } = renderRouter('/w/acme-status/monitors/m1?window=6h');

		expect(
			await screen.findByRole('heading', { name: 'API' }),
		).toBeInTheDocument();
		await waitFor(() => expect(summaryWindows).toContain('6h'));
		expect(resultsWindows).toContain('6h');
		expect(router.state.location.search).toEqual({ window: '6h' });
		expect(screen.getByText('Never checked')).toBeInTheDocument();
	});

	test('an invalid detail window search falls back to 24h', async () => {
		const summaryWindows: (string | null)[] = [];
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(memberMe)),
			http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors/m1`, () =>
				HttpResponse.json(detailMonitor),
			),
			http.get(
				`${ORIGIN}/api/v1/workspaces/w1/monitors/m1/summary`,
				({ request }) => {
					summaryWindows.push(new URL(request.url).searchParams.get('window'));
					return HttpResponse.json(detailSummary);
				},
			),
			http.get(`${ORIGIN}/api/v1/workspaces/w1/monitors/m1/results`, () =>
				HttpResponse.json({ results: [] }),
			),
		);

		renderRouter('/w/acme-status/monitors/m1?window=bogus');

		expect(
			await screen.findByRole('heading', { name: 'API' }),
		).toBeInTheDocument();
		await waitFor(() => expect(summaryWindows).toContain('24h'));
		expect(summaryWindows).not.toContain('bogus');
	});
});
