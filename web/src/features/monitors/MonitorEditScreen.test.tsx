import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { act, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import type { Monitor, UpdateMonitorInput, Workspace } from '@/lib/api/types';
import { MonitorEditScreen } from './MonitorEditScreen';

const ORIGIN = 'http://localhost:5173';

Element.prototype.hasPointerCapture = () => false;
Element.prototype.setPointerCapture = () => undefined;
Element.prototype.releasePointerCapture = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const harness = vi.hoisted(() => ({
	navigate: vi.fn(),
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

const monitor: Monitor = {
	id: 'm1',
	workspace_id: workspace.id,
	name: 'API',
	url: 'https://api.example.com/health',
	method: 'POST',
	headers: { Authorization: '***', 'X-Api-Key': '***' },
	has_body: true,
	content_type: 'application/json',
	expected_status: '2xx',
	interval_seconds: 300,
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

const monitorPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/monitors/${monitor.id}`;

function problem(
	code: string,
	status: number,
	title: string,
	fields?: Record<string, string>,
) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title,
			status,
			code,
			...(fields ? { fields } : {}),
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

function useMonitorHandlers(onPatch: (body: UpdateMonitorInput) => void) {
	server.use(
		http.get(monitorPath, () => HttpResponse.json(monitor)),
		http.patch(monitorPath, async ({ request }) => {
			const body = (await request.json()) as UpdateMonitorInput;
			onPatch(body);
			return HttpResponse.json({
				...monitor,
				name: body.name ?? monitor.name,
				updated_at: '2026-01-02T00:00:00Z',
			});
		}),
	);
}

describe('MonitorEditScreen', () => {
	beforeEach(() => {
		harness.navigate.mockReset();
		harness.navigate.mockResolvedValue(undefined);
	});

	test('submitting without header replacement omits headers and never sends masked values', async () => {
		const patches: UpdateMonitorInput[] = [];
		useMonitorHandlers((body) => patches.push(body));
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		const name = await screen.findByLabelText('Name');
		await user.clear(name);
		await user.type(name, 'Renamed');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(patches).toHaveLength(1));

		expect(patches[0]).toEqual({ name: 'Renamed' });
		expect(patches[0]).not.toHaveProperty('headers');
		expect(patches[0]).not.toHaveProperty('body');
		expect(patches[0]).not.toHaveProperty('content_type');
		expect(JSON.stringify(patches[0])).not.toContain('***');
		expect(harness.navigate).toHaveBeenCalledWith({
			href: `/w/${workspace.slug}/monitors/${monitor.id}`,
		});
	});

	test('sends complete replacement rows and a replaced body without masked values', async () => {
		const patches: UpdateMonitorInput[] = [];
		useMonitorHandlers((body) => patches.push(body));
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		await screen.findByLabelText('Name');
		await user.click(screen.getByRole('switch', { name: 'Replace headers' }));
		await user.click(screen.getByRole('button', { name: 'Add header' }));
		await user.type(screen.getByLabelText('Header 1 name'), 'Authorization');
		await user.type(screen.getByLabelText('Header 1 value'), 'Bearer fresh');
		await user.click(screen.getByRole('radio', { name: 'Replace' }));
		await user.type(screen.getByLabelText('Body'), 'ping=true');
		const contentType = screen.getByLabelText('Content type');
		await user.clear(contentType);
		await user.type(contentType, 'application/json');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(patches).toHaveLength(1));

		expect(patches[0]).toEqual({
			headers: { Authorization: 'Bearer fresh' },
			body: 'ping=true',
			content_type: 'application/json',
		});
		expect(JSON.stringify(patches[0])).not.toContain('***');
	});

	test('clearing the body sends an empty body and omits the content type', async () => {
		const patches: UpdateMonitorInput[] = [];
		useMonitorHandlers((body) => patches.push(body));
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		await screen.findByLabelText('Name');
		await user.click(screen.getByRole('radio', { name: 'Clear' }));
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(patches).toHaveLength(1));

		expect(patches[0]).toEqual({ body: '' });
	});

	test('maps validation fields from the server onto the form', async () => {
		server.use(
			http.get(monitorPath, () => HttpResponse.json(monitor)),
			http.patch(monitorPath, () =>
				problem('validation_error', 400, 'Validation failed', {
					url: 'url is not allowed',
				}),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		await screen.findByLabelText('Name');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		expect(await screen.findByText('url is not allowed')).toBeInTheDocument();
		expect(harness.navigate).not.toHaveBeenCalled();
	});

	test('a failed monitor load shows the problem code and retry action', async () => {
		server.use(
			http.get(monitorPath, () =>
				problem('internal_error', 500, 'Could not load monitor'),
			),
		);

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		expect(await screen.findByText('Code: internal_error')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Try again' }),
		).toBeInTheDocument();
	});

	test('shows a page-level alert for quota errors', async () => {
		server.use(
			http.get(monitorPath, () => HttpResponse.json(monitor)),
			http.patch(monitorPath, () =>
				problem('quota_exceeded', 403, 'Monitor quota reached'),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		await screen.findByLabelText('Name');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		expect(
			await screen.findByText('Monitor quota reached'),
		).toBeInTheDocument();
		expect(harness.navigate).not.toHaveBeenCalled();
	});

	test('switching a body-capable monitor to GET clears the body instead of sending it', async () => {
		const patches: UpdateMonitorInput[] = [];
		useMonitorHandlers((body) => patches.push(body));
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
		);

		await screen.findByLabelText('Name');
		await user.click(screen.getByRole('radio', { name: 'Replace' }));
		await user.type(screen.getByLabelText('Body'), 'ping=true');
		await user.click(screen.getByRole('combobox', { name: 'Method' }));
		await user.click(await screen.findByRole('option', { name: 'GET' }));

		expect(
			screen.getByText(
				'GET and HEAD requests cannot have a body. Saving will clear the body and content type.',
			),
		).toBeInTheDocument();
		expect(screen.queryByLabelText('Body')).not.toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(patches).toHaveLength(1));

		expect(patches[0]).toEqual({ method: 'GET', body: '' });
		expect(JSON.stringify(patches[0])).not.toContain('ping=true');
		expect(patches[0]).not.toHaveProperty('content_type');
	});

	test('a refetched monitor does not make untouched fields dirty', async () => {
		let current: Monitor = monitor;
		const patches: UpdateMonitorInput[] = [];
		server.use(
			http.get(monitorPath, () => HttpResponse.json(current)),
			http.patch(monitorPath, async ({ request }) => {
				const body = (await request.json()) as UpdateMonitorInput;
				patches.push(body);
				return HttpResponse.json({
					...current,
					name: body.name ?? current.name,
				});
			}),
		);
		const queryClient = createTestQueryClient();
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorEditScreen workspace={workspace} monitorId={monitor.id} />,
			{ queryClient },
		);

		await screen.findByLabelText('Name');

		current = {
			...monitor,
			url: 'https://remote.example.com/changed',
			interval_seconds: 900,
			timeout_seconds: 20,
		};
		await act(async () => {
			await queryClient.refetchQueries({
				queryKey: queryKeys.monitor(workspace.id, monitor.id),
			});
		});
		await waitFor(() =>
			expect(
				queryClient.getQueryData<Monitor>(
					queryKeys.monitor(workspace.id, monitor.id),
				),
			).toMatchObject({ url: 'https://remote.example.com/changed' }),
		);

		const name = screen.getByLabelText('Name');
		await user.clear(name);
		await user.type(name, 'Renamed');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(patches).toHaveLength(1));

		expect(patches[0]).toEqual({ name: 'Renamed' });
	});
});
