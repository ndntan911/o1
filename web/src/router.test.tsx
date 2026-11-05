import { afterEach, describe, expect, test } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import {
	RouterProvider,
	createMemoryHistory,
	type RouterHistory,
} from '@tanstack/react-router';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { setUnauthenticatedHandler } from '@/lib/api/client';
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

	return router;
}

describe('router guards', () => {
	afterEach(() => {
		setUnauthenticatedHandler(null);
	});

	test('logged-out / redirects to /login and preserves the destination', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => unauthenticatedProblem()));

		const router = renderRouter('/');

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(router.state.location.searchStr).toBe('?redirect=%2F');
		expect(router.state.location.search).toEqual({ redirect: '/' });
	});

	test('logged-out users can open the login screen', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => unauthenticatedProblem()));

		const router = renderRouter('/login');

		expect(
			await screen.findByRole('button', { name: 'Sign in' }),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/login');
		expect(screen.getByLabelText('Email')).toBeInTheDocument();
		expect(screen.getByLabelText('Password')).toBeInTheDocument();
	});

	test('authenticated users visiting /login are redirected home', async () => {
		server.use(http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(me)));

		const router = renderRouter('/login');

		expect(
			await screen.findByRole('heading', {
				name: 'o1 uptime dashboard',
			}),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe('/');
	});
});
