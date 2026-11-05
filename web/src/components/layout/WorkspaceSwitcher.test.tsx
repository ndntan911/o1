import { describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
	RouterProvider,
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
} from '@tanstack/react-router';
import type { Workspace } from '@/lib/api/types';
import { WorkspaceSwitcher } from './WorkspaceSwitcher';

Element.prototype.hasPointerCapture = () => false;
Element.prototype.setPointerCapture = () => undefined;
Element.prototype.releasePointerCapture = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const acme: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'owner',
};

const beta: Workspace = {
	id: 'w2',
	name: 'Beta Status',
	slug: 'beta-status',
	role: 'member',
};

function renderSwitcher(onNavigate?: () => void) {
	const rootRoute = createRootRoute({
		component: () => (
			<WorkspaceSwitcher
				workspaces={[acme, beta]}
				current={acme}
				onNavigate={onNavigate}
			/>
		),
	});
	const monitorsRoute = createRoute({
		getParentRoute: () => rootRoute,
		path: '/w/$workspaceSlug/monitors',
		component: () => <div>Monitors page</div>,
	});
	const workspacesRoute = createRoute({
		getParentRoute: () => rootRoute,
		path: '/workspaces',
		component: () => <div>Workspaces page</div>,
	});
	const router = createRouter({
		routeTree: rootRoute.addChildren([monitorsRoute, workspacesRoute]),
		history: createMemoryHistory({
			initialEntries: ['/w/acme-status/monitors'],
		}),
	});

	render(<RouterProvider router={router} />);
	return router;
}

describe('WorkspaceSwitcher', () => {
	test('selecting a workspace navigates to its monitors', async () => {
		const user = userEvent.setup();
		const onNavigate = vi.fn();
		const router = renderSwitcher(onNavigate);

		await user.click(
			await screen.findByRole('button', { name: 'Workspace switcher' }),
		);
		await user.click(
			await screen.findByRole('menuitem', { name: 'Beta Status' }),
		);

		await waitFor(() =>
			expect(router.state.location.pathname).toBe('/w/beta-status/monitors'),
		);
		expect(onNavigate).toHaveBeenCalledTimes(1);
	});

	test('All workspaces navigates to the workspace list', async () => {
		const user = userEvent.setup();
		const onNavigate = vi.fn();
		const router = renderSwitcher(onNavigate);

		await user.click(
			await screen.findByRole('button', { name: 'Workspace switcher' }),
		);
		await user.click(
			await screen.findByRole('menuitem', { name: 'All workspaces' }),
		);

		await waitFor(() =>
			expect(router.state.location.pathname).toBe('/workspaces'),
		);
		expect(onNavigate).toHaveBeenCalledTimes(1);
	});
});
