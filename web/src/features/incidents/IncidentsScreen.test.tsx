import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { Incident, Workspace } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { IncidentsScreen } from './IncidentsScreen';

const ORIGIN = 'http://localhost:5173';

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
		useParams: () => ({ workspaceSlug: 'acme-status' }),
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

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'member',
};

const incidentsPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/incidents`;

function makeIncident(overrides: Partial<Incident> = {}): Incident {
	return {
		id: 'i1',
		workspace_id: workspace.id,
		monitor_id: 'm1',
		monitor_name: 'API',
		status: 'open',
		started_at: new Date(Date.now() - 2 * 60 * 60_000).toISOString(),
		acknowledged_at: null,
		acknowledged_by: null,
		resolved_at: null,
		last_notified_at: null,
		reminder_count: 0,
		...overrides,
	};
}

function listHandler(
	first: { incidents: Incident[]; next_cursor?: string },
	second?: { incidents: Incident[]; next_cursor?: string },
	requests: URLSearchParams[] = [],
) {
	return http.get(incidentsPath, ({ request }) => {
		const params = new URL(request.url).searchParams;
		requests.push(params);
		return HttpResponse.json(params.get('cursor') ? second : first);
	});
}

describe('IncidentsScreen', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	test('defaults to the Active filter and requests it', async () => {
		const requests: URLSearchParams[] = [];
		server.use(
			listHandler({ incidents: [makeIncident()] }, undefined, requests),
		);

		renderWithProviders(<IncidentsScreen workspace={workspace} />);

		expect(await screen.findByText('API')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Active' })).toHaveAttribute(
			'aria-pressed',
			'true',
		);
		await waitFor(() => expect(requests[0].get('status')).toBe('active'));
	});

	test('renders monitor name, status, started time, and duration', async () => {
		const now = Date.now();
		const startedAt = new Date(now - 2 * 60 * 60_000).toISOString();
		const resolvedAt = new Date(now - 30 * 60_000).toISOString();
		const incident = makeIncident({
			status: 'resolved',
			started_at: startedAt,
			resolved_at: resolvedAt,
		});
		server.use(listHandler({ incidents: [incident] }));

		renderWithProviders(<IncidentsScreen workspace={workspace} />);

		const row = await screen.findByRole('row', { name: /API/ });
		expect(within(row).getByText('API')).toBeInTheDocument();
		expect(within(row).getByText('Resolved')).toBeInTheDocument();
		expect(
			within(row).getByText(formatRelativeTime(startedAt)),
		).toBeInTheDocument();
		expect(within(row).getByText('1h 30m')).toBeInTheDocument();
		expect(
			within(row).getByRole('link', { name: 'View API incident' }),
		).toHaveAttribute('href', '/w/acme-status/incidents/i1');
	});

	test('changing the filter requests the chosen status', async () => {
		const requests: URLSearchParams[] = [];
		server.use(listHandler({ incidents: [] }, undefined, requests));
		const user = userEvent.setup();

		renderWithProviders(<IncidentsScreen workspace={workspace} />);
		await screen.findByText('No incidents');

		await user.click(screen.getByRole('button', { name: 'Open' }));

		await waitFor(() =>
			expect(requests.some((params) => params.get('status') === 'open')).toBe(
				true,
			),
		);
		expect(screen.getByRole('button', { name: 'Open' })).toHaveAttribute(
			'aria-pressed',
			'true',
		);
		expect(screen.getByRole('button', { name: 'Active' })).toHaveAttribute(
			'aria-pressed',
			'false',
		);
	});

	test('Load more requests the cursor from the first page and appends it', async () => {
		const requests: URLSearchParams[] = [];
		server.use(
			listHandler(
				{ incidents: [makeIncident()], next_cursor: 'page-2' },
				{
					incidents: [
						makeIncident({ id: 'i2', monitor_name: 'Second monitor' }),
					],
				},
				requests,
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<IncidentsScreen workspace={workspace} />);
		await screen.findByText('API');

		await user.click(screen.getByRole('button', { name: 'Load more' }));

		expect(await screen.findByText('Second monitor')).toBeInTheDocument();
		expect(requests.map((params) => params.get('cursor'))).toEqual([
			null,
			'page-2',
		]);
		expect(
			screen.queryByRole('button', { name: 'Load more' }),
		).not.toBeInTheDocument();
	});

	test('the empty state shows No incidents', async () => {
		server.use(listHandler({ incidents: [] }));

		renderWithProviders(<IncidentsScreen workspace={workspace} />);

		expect(await screen.findByText('No incidents')).toBeInTheDocument();
	});

	test('a viewer sees no acknowledge action in the list', async () => {
		server.use(listHandler({ incidents: [makeIncident()] }));

		renderWithProviders(
			<IncidentsScreen workspace={{ ...workspace, role: 'viewer' }} />,
		);

		expect(await screen.findByText('API')).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Acknowledge' }),
		).not.toBeInTheDocument();
	});
});
