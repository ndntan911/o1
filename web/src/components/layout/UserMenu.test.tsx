import { describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import {
	RouterProvider,
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
} from '@tanstack/react-router';
import { ThemeProvider } from 'next-themes';
import { UserMenu } from './UserMenu';

vi.mock('@/features/workspaces/hooks', () => ({
	useLogout: () => ({ mutate: vi.fn(), isPending: false }),
}));

function renderUserMenu() {
	const rootRoute = createRootRoute({
		component: () => <UserMenu email="owner@example.com" />,
	});
	const securityRoute = createRoute({
		getParentRoute: () => rootRoute,
		path: '/account/security',
		component: () => <p>security route</p>,
	});
	const router = createRouter({
		routeTree: rootRoute.addChildren([securityRoute]),
		history: createMemoryHistory({ initialEntries: ['/'] }),
	});

	render(
		<ThemeProvider
			attribute="class"
			defaultTheme="system"
			enableSystem
			storageKey="o1-theme"
		>
			<RouterProvider router={router} />
		</ThemeProvider>,
	);
}

describe('UserMenu', () => {
	test('shows the account and the theme control', async () => {
		renderUserMenu();

		expect(await screen.findByText('owner@example.com')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Log out' })).toBeEnabled();
		expect(screen.getByRole('group', { name: 'Theme' })).toBeInTheDocument();
	});

	test('links to the account security page', async () => {
		renderUserMenu();

		expect(
			await screen.findByRole('link', { name: 'Security' }),
		).toHaveAttribute('href', '/account/security');
	});
});
