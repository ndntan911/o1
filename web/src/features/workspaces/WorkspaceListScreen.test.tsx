import type { AnchorHTMLAttributes, ReactElement, ReactNode } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import { resetWorkspaceStatusForTests } from '@/lib/workspace-status';
import { queryKeys } from '@/lib/query/keys';
import type { Me, Workspace } from '@/lib/api/types';
import { AppShell } from '@/components/layout/AppShell';
import { WorkspaceListScreen } from './WorkspaceListScreen';

const ORIGIN = 'http://localhost:5173';

const harness = vi.hoisted(() => ({
	navigate: vi.fn(),
	outlet: { current: null as ReactNode },
}));

vi.mock('@tanstack/react-router', async (importOriginal) => {
	const actual =
		await importOriginal<typeof import('@tanstack/react-router')>();

	function buildHref(to: string, params?: Record<string, unknown>): string {
		let href = to;
		for (const [key, value] of Object.entries(params ?? {})) {
			href = href.replace(`$${key}`, String(value));
		}
		return href;
	}

	return {
		...actual,
		useNavigate: () => harness.navigate,
		useParams: () => ({}),
		Outlet: () => harness.outlet.current as ReactElement,
		Link: ({
			to,
			params,
			children,
			...props
		}: {
			to?: string;
			params?: Record<string, unknown>;
			children?: ReactNode;
		} & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'>) => (
			<a href={to ? buildHref(to, params) : undefined} {...props}>
				{children}
			</a>
		),
	};
});

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

const me: Me = {
	user: { id: 'u1', email: 'alice@example.com', is_admin: false },
	workspaces: [acme],
};

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title,
			status,
			code,
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

describe('WorkspaceListScreen', () => {
	beforeEach(() => {
		harness.navigate.mockReset();
		harness.outlet.current = null;
		resetWorkspaceStatusForTests();
	});

	test('renders a card per workspace with its role and a monitors link', () => {
		renderWithProviders(<WorkspaceListScreen workspaces={[acme, beta]} />);

		expect(screen.getByText('Acme Status')).toBeInTheDocument();
		expect(screen.getByText('Beta Status')).toBeInTheDocument();
		expect(screen.getByText('owner')).toBeInTheDocument();
		expect(screen.getByText('member')).toBeInTheDocument();

		const links = screen.getAllByRole('link', { name: 'View monitors' });
		expect(links.map((link) => link.getAttribute('href'))).toEqual([
			'/w/acme-status/monitors',
			'/w/beta-status/monitors',
		]);
	});

	test('shows the empty state with a new workspace action', () => {
		renderWithProviders(<WorkspaceListScreen workspaces={[]} />);

		expect(screen.getByText('No workspaces yet')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'New workspace' }),
		).toBeInTheDocument();
	});

	test('creates a workspace, invalidates me, and navigates to its monitors', async () => {
		const bodies: unknown[] = [];
		const created: Workspace = {
			id: 'w3',
			name: 'Gamma Status',
			slug: 'gamma-status',
			role: 'owner',
		};
		server.use(
			http.post(`${ORIGIN}/api/v1/workspaces`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(created, { status: 201 });
			}),
		);

		const queryClient = createTestQueryClient();
		queryClient.setQueryData(queryKeys.me, me);
		const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
		const user = userEvent.setup();
		renderWithProviders(<WorkspaceListScreen workspaces={[acme]} />, {
			queryClient,
		});

		await user.click(screen.getByRole('button', { name: 'New workspace' }));
		await user.type(screen.getByLabelText('Name'), 'Gamma Status');
		await user.click(screen.getByRole('button', { name: 'Create workspace' }));

		await waitFor(() =>
			expect(harness.navigate).toHaveBeenCalledWith({
				to: '/w/$workspaceSlug/monitors',
				params: { workspaceSlug: 'gamma-status' },
			}),
		);
		expect(bodies).toEqual([{ name: 'Gamma Status' }]);
		expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.me });
		expect(
			queryClient
				.getQueryData<Me>(queryKeys.me)
				?.workspaces.some((workspace) => workspace.slug === 'gamma-status'),
		).toBe(true);
	});

	test('workspace_suspended from a mutation shows the persistent suspension banner', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/me`, () => HttpResponse.json(me)),
			http.post(`${ORIGIN}/api/v1/workspaces`, () =>
				problem('workspace_suspended', 403, 'Request failed'),
			),
		);

		const queryClient = createTestQueryClient();
		queryClient.setQueryData(queryKeys.me, me);
		harness.outlet.current = <WorkspaceListScreen workspaces={[]} />;
		const user = userEvent.setup();
		renderWithProviders(<AppShell />, { queryClient });

		await user.click(
			await screen.findByRole('button', { name: 'New workspace' }),
		);
		await user.type(screen.getByLabelText('Name'), 'Blocked workspace');
		await user.click(screen.getByRole('button', { name: 'Create workspace' }));

		expect(await screen.findByText('Workspace suspended')).toBeInTheDocument();
	});
});
