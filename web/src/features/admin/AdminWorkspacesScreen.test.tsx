import { describe, expect, test } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type {
	AdminWorkspace,
	AdminWorkspaceListResponse,
} from '@/lib/api/types';
import { AdminWorkspacesScreen } from './AdminWorkspacesScreen';

const ORIGIN = 'http://localhost:5173';
const listPath = `${ORIGIN}/api/v1/admin/workspaces`;

const activeWorkspace: AdminWorkspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	created_by: 'u1',
	created_at: '2026-01-02T10:00:00Z',
	monitor_limit: 10,
	suspended_at: null,
	suspended_by: null,
	suspended_reason: null,
	member_count: 3,
	monitor_count: 2,
};

const suspendedWorkspace: AdminWorkspace = {
	...activeWorkspace,
	id: 'w2',
	name: 'Beta Status',
	slug: 'beta-status',
	suspended_at: '2026-02-03T10:00:00Z',
	suspended_by: 'u9',
	suspended_reason: 'Payment overdue',
	member_count: 1,
	monitor_count: 4,
};

interface Recorded {
	urls: URL[];
	suspends: { path: string; body: unknown }[];
	unsuspends: string[];
}

function setupHandlers(
	workspaces: AdminWorkspace[] = [activeWorkspace, suspendedWorkspace],
): Recorded {
	const recorded: Recorded = { urls: [], suspends: [], unsuspends: [] };
	server.use(
		http.get(listPath, ({ request }) => {
			recorded.urls.push(new URL(request.url));
			const response: AdminWorkspaceListResponse = { workspaces };
			return HttpResponse.json(response);
		}),
		http.post(
			`${ORIGIN}/api/v1/admin/workspaces/:id/suspend`,
			async ({ request, params }) => {
				recorded.suspends.push({
					path: String(params.id),
					body: await request.json(),
				});
				return new HttpResponse(null, { status: 204 });
			},
		),
		http.post(
			`${ORIGIN}/api/v1/admin/workspaces/:id/unsuspend`,
			({ params }) => {
				recorded.unsuspends.push(String(params.id));
				return new HttpResponse(null, { status: 204 });
			},
		),
	);
	return recorded;
}

function rowFor(name: RegExp) {
	return screen.getByRole('row', { name });
}

describe('AdminWorkspacesScreen', () => {
	test('renders workspace name, slug, counts, and suspension state', async () => {
		setupHandlers();

		renderWithProviders(<AdminWorkspacesScreen />);

		expect(
			await screen.findByRole('heading', { name: 'Workspaces' }),
		).toBeInTheDocument();
		await screen.findByText('Acme Status');

		const activeRow = rowFor(/Acme Status/);
		expect(within(activeRow).getByText('acme-status')).toBeInTheDocument();
		expect(within(activeRow).getByText('3')).toBeInTheDocument();
		expect(within(activeRow).getByText('2')).toBeInTheDocument();
		expect(within(activeRow).getByText('Active')).toBeInTheDocument();

		const suspendedRow = rowFor(/Beta Status/);
		expect(within(suspendedRow).getByText('beta-status')).toBeInTheDocument();
		expect(within(suspendedRow).getByText('1')).toBeInTheDocument();
		expect(within(suspendedRow).getByText('4')).toBeInTheDocument();
		expect(within(suspendedRow).getByText('Suspended')).toBeInTheDocument();
		expect(
			within(suspendedRow).getByText('Payment overdue'),
		).toBeInTheDocument();
	});

	test('search updates the q query parameter after the debounce', async () => {
		const user = userEvent.setup();
		const recorded = setupHandlers();

		renderWithProviders(<AdminWorkspacesScreen />);
		await screen.findByText('Acme Status');

		await user.type(screen.getByPlaceholderText('Search workspaces'), 'acme');

		await waitFor(
			() => {
				expect(recorded.urls.at(-1)?.searchParams.get('q')).toBe('acme');
			},
			{ timeout: 2000 },
		);
	});

	test('the status filter sends suspended=true or suspended=false', async () => {
		const user = userEvent.setup();
		const recorded = setupHandlers();

		renderWithProviders(<AdminWorkspacesScreen />);
		await screen.findByText('Acme Status');

		const status = screen.getByLabelText('Status');
		expect(within(status).getByRole('option', { name: 'All' })).toBeVisible();
		expect(
			within(status).getByRole('option', { name: 'Active' }),
		).toBeVisible();
		expect(
			within(status).getByRole('option', { name: 'Suspended' }),
		).toBeVisible();

		await user.selectOptions(status, 'suspended');
		await waitFor(
			() => {
				expect(recorded.urls.at(-1)?.searchParams.get('suspended')).toBe(
					'true',
				);
			},
			{ timeout: 2000 },
		);

		await user.selectOptions(status, 'active');
		await waitFor(
			() => {
				expect(recorded.urls.at(-1)?.searchParams.get('suspended')).toBe(
					'false',
				);
			},
			{ timeout: 2000 },
		);
	});

	test('suspend requires a reason, posts it, and refreshes the list', async () => {
		const user = userEvent.setup();
		const recorded = setupHandlers();

		renderWithProviders(<AdminWorkspacesScreen />);
		const activeRow = await screen.findByRole('row', { name: /Acme Status/ });
		const loadsBefore = recorded.urls.length;

		await user.click(
			within(activeRow).getByRole('button', { name: 'Suspend' }),
		);

		expect(screen.getByLabelText('Reason')).toBeInTheDocument();
		const submit = screen.getByRole('button', { name: 'Suspend workspace' });
		expect(submit).toBeDisabled();

		await user.type(screen.getByLabelText('Reason'), 'Abuse report');
		await user.click(submit);

		await waitFor(() => expect(recorded.suspends).toHaveLength(1));
		expect(recorded.suspends[0]).toEqual({
			path: 'w1',
			body: { reason: 'Abuse report' },
		});
		await waitFor(() =>
			expect(recorded.urls.length).toBeGreaterThan(loadsBefore),
		);
	});

	test('unsuspend asks for confirmation and posts to unsuspend', async () => {
		const user = userEvent.setup();
		const recorded = setupHandlers();

		renderWithProviders(<AdminWorkspacesScreen />);
		const suspendedRow = await screen.findByRole('row', {
			name: /Beta Status/,
		});

		await user.click(
			within(suspendedRow).getByRole('button', { name: 'Unsuspend' }),
		);

		expect(screen.getByRole('dialog')).toBeInTheDocument();
		await user.click(
			screen.getByRole('button', { name: 'Unsuspend workspace' }),
		);

		await waitFor(() => expect(recorded.unsuspends).toEqual(['w2']));
	});

	test('a failed unsuspend keeps the dialog open and shows the error', async () => {
		const user = userEvent.setup();
		setupHandlers();
		server.use(
			http.post(`${ORIGIN}/api/v1/admin/workspaces/:id/unsuspend`, () =>
				HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Internal Server Error',
						status: 500,
						code: 'internal_error',
					},
					{
						status: 500,
						headers: { 'Content-Type': 'application/problem+json' },
					},
				),
			),
		);

		renderWithProviders(<AdminWorkspacesScreen />);
		const suspendedRow = await screen.findByRole('row', {
			name: /Beta Status/,
		});

		await user.click(
			within(suspendedRow).getByRole('button', { name: 'Unsuspend' }),
		);
		await user.click(
			screen.getByRole('button', { name: 'Unsuspend workspace' }),
		);

		const dialog = await screen.findByRole('dialog');
		expect(
			within(dialog).getByRole('heading', { name: 'Unsuspend Beta Status?' }),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText('Code: internal_error'),
		).toBeInTheDocument();
	});
});
