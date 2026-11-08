import { beforeEach, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { Monitor, StatusPageConfig } from '@/lib/api/types';
import { StatusPageSettingsScreen } from './StatusPageSettingsScreen';

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const ORIGIN = 'http://localhost:5173';
const workspace = {
	id: 'w1',
	name: 'Acme',
	slug: 'acme',
	role: 'admin' as const,
};
const configPath = `${ORIGIN}/api/v1/workspaces/w1/status-page`;
const monitorsPath = `${ORIGIN}/api/v1/workspaces/w1/monitors`;

const monitor: Monitor = {
	id: 'm1',
	workspace_id: 'w1',
	name: 'API',
	url: 'https://secret.example.com/health',
	method: 'GET',
	headers: {},
	has_body: false,
	content_type: null,
	expected_status: '2xx',
	interval_seconds: 60,
	timeout_seconds: 10,
	confirmation_threshold: 2,
	recovery_threshold: 2,
	paused: false,
	status: 'up',
	last_check: null,
	summary_24h: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

function serveConfig(config: StatusPageConfig) {
	server.use(
		http.get(configPath, () => HttpResponse.json(config)),
		http.get(monitorsPath, () => HttpResponse.json({ monitors: [monitor] })),
	);
}

beforeEach(() => {
	toast.success.mockReset();
	toast.error.mockReset();
});

test('loads defaults, selects a monitor, and saves', async () => {
	const user = userEvent.setup();
	const bodies: unknown[] = [];
	serveConfig({
		enabled: false,
		title: 'Acme',
		description: '',
		monitor_ids: [],
	});
	server.use(
		http.put(configPath, async ({ request }) => {
			bodies.push(await request.json());
			return HttpResponse.json({
				enabled: true,
				title: 'Acme status',
				description: 'Live',
				monitor_ids: ['m1'],
			});
		}),
	);

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	expect(await screen.findByDisplayValue('Acme')).toBeInTheDocument();
	await user.click(screen.getByLabelText('Publish API'));
	await user.click(screen.getByRole('switch', { name: 'Status page enabled' }));
	await user.clear(screen.getByLabelText('Title'));
	await user.type(screen.getByLabelText('Title'), 'Acme status');
	await user.type(screen.getByLabelText('Description'), 'Live');
	await user.click(screen.getByRole('button', { name: 'Save status page' }));

	await waitFor(() =>
		expect(bodies).toEqual([
			{
				enabled: true,
				title: 'Acme status',
				description: 'Live',
				monitor_ids: ['m1'],
			},
		]),
	);
	expect(toast.success).toHaveBeenCalledWith('Status page saved');
});

test('blocks enabling with no monitors', async () => {
	const user = userEvent.setup();
	serveConfig({
		enabled: false,
		title: 'Acme',
		description: '',
		monitor_ids: [],
	});
	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	await screen.findByDisplayValue('Acme');
	await user.click(screen.getByRole('switch', { name: 'Status page enabled' }));
	await user.click(screen.getByRole('button', { name: 'Save status page' }));
	expect(
		await screen.findByText(
			'Select at least one monitor before enabling the page.',
		),
	).toBeInTheDocument();
});

test('shows server field errors and suspended problems', async () => {
	const user = userEvent.setup();
	serveConfig({
		enabled: false,
		title: 'Acme',
		description: '',
		monitor_ids: [],
	});
	server.use(
		http.put(configPath, () =>
			HttpResponse.json(
				{
					type: 'about:blank',
					title: 'Validation Error',
					status: 400,
					code: 'validation_error',
					fields: { title: 'title is required' },
				},
				{
					status: 400,
					headers: { 'Content-Type': 'application/problem+json' },
				},
			),
		),
	);

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	await screen.findByDisplayValue('Acme');
	await user.click(screen.getByRole('button', { name: 'Save status page' }));
	expect(await screen.findByText('title is required')).toBeInTheDocument();
});

test('shows the public link and copies it', async () => {
	const user = userEvent.setup();
	serveConfig({
		enabled: true,
		title: 'Acme status',
		description: '',
		monitor_ids: ['m1'],
	});
	const writeText = vi.fn().mockResolvedValue(undefined);
	Object.defineProperty(navigator, 'clipboard', {
		configurable: true,
		value: { writeText },
	});

	renderWithProviders(<StatusPageSettingsScreen workspace={workspace} />);
	const link = await screen.findByDisplayValue(`${ORIGIN}/status/acme`);
	expect(link).toBeInTheDocument();
	await user.click(screen.getByRole('button', { name: 'Copy public link' }));
	expect(writeText).toHaveBeenCalledWith(`${ORIGIN}/status/acme`);
	expect(toast.success).toHaveBeenCalledWith('Public link copied');
});
