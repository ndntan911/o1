import { afterEach, describe, expect, test } from 'vitest';
import { act, render, screen } from '@testing-library/react';
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
});
