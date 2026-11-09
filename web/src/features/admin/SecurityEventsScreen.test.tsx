import { describe, expect, test } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type {
	AdminWorkspaceListResponse,
	SecurityEvent,
	SecurityEventListResponse,
} from '@/lib/api/types';
import { SECURITY_EVENT_KINDS } from './api';
import { SecurityEventsScreen } from './SecurityEventsScreen';

const ORIGIN = 'http://localhost:5173';
const eventsPath = `${ORIGIN}/api/v1/admin/security-events`;
const workspacesPath = `${ORIGIN}/api/v1/admin/workspaces`;

const detail = {
	url: 'https://example.com/health?token=***',
	headers: { authorization: '***' },
	reason: 'ssrf_blocked',
};

const events: SecurityEvent[] = [
	{
		id: 'e1',
		workspace_id: 'w1',
		actor_user_id: 'u1',
		actor_api_key_id: null,
		kind: 'ssrf_blocked',
		detail,
		created_at: '2026-02-03T10:00:00Z',
	},
	{
		id: 'e2',
		workspace_id: null,
		actor_user_id: null,
		actor_api_key_id: null,
		kind: 'monitor_created',
		detail: { monitor_id: 'm1' },
		created_at: '2026-02-02T10:00:00Z',
	},
];

const auditKinds = [
	'login_succeeded',
	'login_failed',
	'member_added',
	'member_role_changed',
	'monitor_deleted',
	'alert_channel_created',
	'alert_channel_updated',
	'alert_channel_deleted',
	'status_page_updated',
	'api_key_created',
	'api_key_revoked',
	'totp_enabled',
	'totp_disabled',
	'totp_recovery_codes_regenerated',
] as const;

const workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	created_by: 'u1',
	created_at: '2026-01-01T00:00:00Z',
	monitor_limit: 10,
	suspended_at: null,
	suspended_by: null,
	suspended_reason: null,
	member_count: 2,
	monitor_count: 3,
};

function setupHandlers() {
	const urls: URL[] = [];
	server.use(
		http.get(eventsPath, ({ request }) => {
			urls.push(new URL(request.url));
			const response: SecurityEventListResponse = { events };
			return HttpResponse.json(response);
		}),
		http.get(workspacesPath, () => {
			const response: AdminWorkspaceListResponse = {
				workspaces: [workspace],
			};
			return HttpResponse.json(response);
		}),
	);
	return urls;
}

describe('SecurityEventsScreen', () => {
	test('renders the audit kinds and sends the selected kind', async () => {
		const user = userEvent.setup();
		const urls = setupHandlers();

		renderWithProviders(<SecurityEventsScreen />);

		expect(
			await screen.findByRole('heading', { name: 'Security events' }),
		).toBeInTheDocument();

		const kind = screen.getByLabelText('Kind');
		expect(
			within(kind).getByRole('option', { name: 'All kinds' }),
		).toBeVisible();
		for (const value of SECURITY_EVENT_KINDS) {
			expect(within(kind).getByRole('option', { name: value })).toBeVisible();
		}
		for (const value of auditKinds) {
			expect(within(kind).getByRole('option', { name: value })).toBeVisible();
		}

		await user.selectOptions(kind, 'ssrf_blocked');

		await waitFor(() => {
			expect(urls.at(-1)?.searchParams.get('kind')).toBe('ssrf_blocked');
		});

		for (const value of [
			'totp_enabled',
			'totp_disabled',
			'totp_recovery_codes_regenerated',
		] as const) {
			await user.selectOptions(kind, value);
			await waitFor(() => {
				expect(urls.at(-1)?.searchParams.get('kind')).toBe(value);
			});
		}
	});

	test('the workspace combobox sends workspace_id', async () => {
		const user = userEvent.setup();
		const urls = setupHandlers();

		renderWithProviders(<SecurityEventsScreen />);
		await screen.findByRole('list', { name: 'Security events' });

		await user.type(screen.getByLabelText('Workspace'), 'acme');
		const option = await screen.findByRole(
			'option',
			{ name: /Acme Status/ },
			{ timeout: 2000 },
		);
		await user.click(option);

		await waitFor(
			() => {
				expect(urls.at(-1)?.searchParams.get('workspace_id')).toBe('w1');
			},
			{ timeout: 2000 },
		);
	});

	test('shows API key attribution only when the event has one', async () => {
		server.use(
			http.get(eventsPath, () =>
				HttpResponse.json({
					events: [
						{ ...events[0], id: 'k1', actor_api_key_id: 'key-1' },
						{ ...events[1], id: 'k2', actor_api_key_id: null },
					],
				} satisfies SecurityEventListResponse),
			),
			http.get(workspacesPath, () =>
				HttpResponse.json({
					workspaces: [workspace],
				} satisfies AdminWorkspaceListResponse),
			),
		);

		renderWithProviders(<SecurityEventsScreen />);

		const list = await screen.findByRole('list', { name: 'Security events' });
		const items = within(list).getAllByRole('listitem');

		expect(within(items[0]).getByText(/via API key key-1/)).toBeInTheDocument();
		expect(within(items[1]).queryByText(/via API key/)).not.toBeInTheDocument();
	});

	test('the detail viewer renders the redacted JSON exactly', async () => {
		const user = userEvent.setup();
		setupHandlers();

		renderWithProviders(<SecurityEventsScreen />);

		const list = await screen.findByRole('list', { name: 'Security events' });
		const firstEvent = within(list).getAllByRole('listitem')[0];
		expect(within(firstEvent).getByText('ssrf_blocked')).toBeInTheDocument();
		expect(
			within(firstEvent).queryByText(/"authorization"/),
		).not.toBeInTheDocument();

		await user.click(
			within(firstEvent).getByRole('button', { name: 'Details' }),
		);

		const pre = within(firstEvent).getByText(/"authorization"/);
		expect(pre.tagName).toBe('PRE');
		expect(pre).toHaveTextContent('"authorization": "***"');
		expect(pre).toHaveTextContent('https://example.com/health?token=***');
		expect(pre).not.toHaveTextContent('Bearer');
	});
});
