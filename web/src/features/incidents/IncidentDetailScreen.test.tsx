import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type {
	Incident,
	IncidentDelivery,
	IncidentDetail,
	IncidentEvent,
	Workspace,
} from '@/lib/api/types';
import { IncidentDetailScreen } from './IncidentDetailScreen';

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

const incidentPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/incidents/i1`;
const acknowledgePath = `${incidentPath}/acknowledge`;
const deliveriesPath = `${incidentPath}/deliveries`;

function makeIncident(overrides: Partial<Incident> = {}): Incident {
	return {
		id: 'i1',
		workspace_id: workspace.id,
		monitor_id: 'm1',
		monitor_name: 'API',
		status: 'open',
		started_at: '2026-01-01T00:00:00Z',
		acknowledged_at: null,
		acknowledged_by: null,
		resolved_at: null,
		last_notified_at: null,
		reminder_count: 0,
		...overrides,
	};
}

function makeEvent(
	kind: IncidentEvent['kind'],
	createdAt: string,
	overrides: Partial<IncidentEvent> = {},
): IncidentEvent {
	return {
		id: `${kind}-event`,
		kind,
		actor_user_id: kind === 'opened' ? null : 'u1',
		detail: null,
		created_at: createdAt,
		...overrides,
	};
}

function makeDelivery(
	overrides: Partial<IncidentDelivery> = {},
): IncidentDelivery {
	return {
		id: 'd1',
		event: 'opened',
		sequence: 1,
		recipient: 'ops@example.com',
		status: 'sent',
		attempts: 1,
		last_error: null,
		sent_at: '2026-01-01T00:01:00Z',
		created_at: '2026-01-01T00:00:00Z',
		...overrides,
	};
}

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

function detailHandler(detail: IncidentDetail) {
	return http.get(incidentPath, () => HttpResponse.json(detail));
}

describe('IncidentDetailScreen', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	test('renders the timeline oldest first', async () => {
		const events = [
			makeEvent('resolved', '2026-01-01T03:00:00Z'),
			makeEvent('opened', '2026-01-01T01:00:00Z'),
			makeEvent('acknowledged', '2026-01-01T02:00:00Z'),
		];
		server.use(
			detailHandler({
				incident: makeIncident({
					status: 'resolved',
					resolved_at: '2026-01-01T03:00:00Z',
				}),
				events,
			}),
		);

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		const timeline = await screen.findByRole('list', {
			name: 'Incident timeline',
		});
		const items = within(timeline).getAllByRole('listitem');

		expect(items).toHaveLength(3);
		expect(items[0]).toHaveTextContent('Opened');
		expect(items[1]).toHaveTextContent('Acknowledged');
		expect(items[2]).toHaveTextContent('Resolved');
	});

	test('shows Acknowledge for a member on an open incident', async () => {
		server.use(
			detailHandler({
				incident: makeIncident(),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
		);

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		expect(
			await screen.findByRole('button', { name: 'Acknowledge' }),
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View monitor' })).toHaveAttribute(
			'href',
			'/w/acme-status/monitors/m1',
		);
	});

	test('hides Acknowledge for a viewer', async () => {
		server.use(
			detailHandler({
				incident: makeIncident(),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
		);

		renderWithProviders(
			<IncidentDetailScreen
				workspace={{ ...workspace, role: 'viewer' }}
				incidentId="i1"
			/>,
		);

		expect(await screen.findByText('Incident: API')).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Acknowledge' }),
		).not.toBeInTheDocument();
	});

	test.each([
		['acknowledged', { status: 'acknowledged' as const }],
		['resolved', { status: 'resolved' as const }],
	])('hides Acknowledge when the incident is %s', async (_label, overrides) => {
		server.use(
			detailHandler({
				incident: makeIncident(overrides),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
		);

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		expect(await screen.findByText('Incident: API')).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Acknowledge' }),
		).not.toBeInTheDocument();
	});

	test('clicking Acknowledge calls POST and refreshes the incident', async () => {
		let acknowledged = false;
		const acknowledgeCalls: string[] = [];
		const openedEvent = makeEvent('opened', '2026-01-01T01:00:00Z');
		const acknowledgedEvent = makeEvent('acknowledged', '2026-01-01T02:00:00Z');
		server.use(
			http.get(incidentPath, () =>
				HttpResponse.json({
					incident: acknowledged
						? makeIncident({
								status: 'acknowledged',
								acknowledged_at: '2026-01-01T02:00:00Z',
								acknowledged_by: 'u1',
							})
						: makeIncident(),
					events: acknowledged
						? [openedEvent, acknowledgedEvent]
						: [openedEvent],
				} satisfies IncidentDetail),
			),
			http.post(acknowledgePath, () => {
				acknowledgeCalls.push('acknowledge');
				acknowledged = true;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		await user.click(
			await screen.findByRole('button', { name: 'Acknowledge' }),
		);

		await waitFor(() => expect(acknowledgeCalls).toHaveLength(1));
		await waitFor(() =>
			expect(
				screen.queryByRole('button', { name: 'Acknowledge' }),
			).not.toBeInTheDocument(),
		);
		expect(screen.getAllByText('Acknowledged').length).toBeGreaterThan(0);
	});

	test('shows a not found state for a missing incident', async () => {
		server.use(
			http.get(incidentPath, () => problem('not_found', 404, 'Not Found')),
		);

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		expect(await screen.findByText('Incident not found')).toBeInTheDocument();
		expect(
			screen.getByText('This incident may have been removed.'),
		).toBeInTheDocument();
	});

	test('an admin sees the alert delivery table', async () => {
		server.use(
			detailHandler({
				incident: makeIncident(),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
			http.get(deliveriesPath, () =>
				HttpResponse.json({
					deliveries: [
						makeDelivery(),
						makeDelivery({
							id: 'd2',
							event: 'reminder',
							sequence: 2,
							recipient: 'oncall@example.com',
							status: 'failed',
							attempts: 3,
							last_error: 'mailbox unavailable',
						}),
					],
				}),
			),
		);

		renderWithProviders(
			<IncidentDetailScreen
				workspace={{ ...workspace, role: 'admin' }}
				incidentId="i1"
			/>,
		);

		expect(
			await screen.findByRole('heading', { name: 'Alert deliveries' }),
		).toBeInTheDocument();
		expect(await screen.findByText('ops@example.com')).toBeInTheDocument();
		expect(screen.getByText('oncall@example.com')).toBeInTheDocument();
		expect(screen.getAllByText('Sent').length).toBeGreaterThan(0);
		expect(screen.getAllByText('Failed').length).toBeGreaterThan(0);
	});

	test('groups deliveries by event and shows last_error', async () => {
		server.use(
			detailHandler({
				incident: makeIncident(),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
			http.get(deliveriesPath, () =>
				HttpResponse.json({
					deliveries: [
						makeDelivery({
							id: 'd1',
							event: 'reminder',
							sequence: 2,
							recipient: 'reminder@example.com',
							status: 'failed',
							attempts: 3,
							last_error: 'mailbox unavailable',
						}),
						makeDelivery({
							id: 'd2',
							recipient: 'opened@example.com',
						}),
						makeDelivery({
							id: 'd3',
							event: 'reminder',
							sequence: 3,
							recipient: 'oncall@example.com',
							status: 'pending',
							attempts: 1,
							last_error: null,
							sent_at: null,
						}),
					],
				}),
			),
		);

		renderWithProviders(
			<IncidentDetailScreen
				workspace={{ ...workspace, role: 'admin' }}
				incidentId="i1"
			/>,
		);

		expect(
			await screen.findByRole('heading', { name: 'Alert deliveries' }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole('heading', { level: 3, name: 'Opened' }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('heading', { level: 3, name: 'Reminder' }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole('heading', { level: 3, name: 'Resolved' }),
		).not.toBeInTheDocument();

		const openedTable = await screen.findByRole('table', {
			name: 'Opened alert deliveries',
		});
		const reminderTable = screen.getByRole('table', {
			name: 'Reminder alert deliveries',
		});
		expect(
			within(openedTable).getByText('opened@example.com'),
		).toBeInTheDocument();
		expect(
			within(openedTable).queryByText('reminder@example.com'),
		).not.toBeInTheDocument();
		expect(
			within(reminderTable).getByText('reminder@example.com'),
		).toBeInTheDocument();
		expect(
			within(reminderTable).getByText('oncall@example.com'),
		).toBeInTheDocument();
		expect(
			within(reminderTable).getByText('mailbox unavailable'),
		).toBeInTheDocument();
		expect(
			screen.getAllByRole('columnheader', { name: 'Last error' }),
		).toHaveLength(2);
	});

	test('a non-admin does not request or render deliveries', async () => {
		let deliveryRequests = 0;
		server.use(
			detailHandler({
				incident: makeIncident(),
				events: [makeEvent('opened', '2026-01-01T01:00:00Z')],
			}),
			http.get(deliveriesPath, () => {
				deliveryRequests += 1;
				return HttpResponse.json({ deliveries: [makeDelivery()] });
			}),
		);

		renderWithProviders(
			<IncidentDetailScreen workspace={workspace} incidentId="i1" />,
		);

		expect(await screen.findByText('Incident: API')).toBeInTheDocument();
		expect(
			screen.queryByRole('heading', { name: 'Alert deliveries' }),
		).not.toBeInTheDocument();
		expect(deliveryRequests).toBe(0);
	});
});
