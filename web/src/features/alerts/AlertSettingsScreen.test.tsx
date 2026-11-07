import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { AlertChannel, Workspace } from '@/lib/api/types';
import { AlertSettingsScreen } from './AlertSettingsScreen';

const ORIGIN = 'http://localhost:5173';

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'admin',
};

const channel: AlertChannel = {
	id: 'c1',
	kind: 'email',
	name: 'Email',
	enabled: true,
	recipients: ['ops@example.com'],
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

const channelsPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/alert-channels`;
const emailChannelPath = `${channelsPath}/email`;

function problem(code: string, status: number, title: string, detail?: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code, detail },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

function channelHandler(list: AlertChannel[]) {
	return http.get(channelsPath, () => HttpResponse.json({ channels: list }));
}

describe('AlertSettingsScreen', () => {
	beforeEach(() => {
		toast.success.mockReset();
		toast.error.mockReset();
	});

	test('an admin sees the recipients editor and enabled switch', async () => {
		server.use(channelHandler([channel]));

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		expect(
			await screen.findByRole('heading', { name: 'Alert settings' }),
		).toBeInTheDocument();
		expect(await screen.findByText('ops@example.com')).toBeInTheDocument();
		expect(screen.getByLabelText('Add recipient')).toBeInTheDocument();
		expect(screen.getByRole('switch', { name: 'Email alerts' })).toBeChecked();
		expect(
			screen.getByRole('button', { name: 'Save changes' }),
		).toBeInTheDocument();
	});

	test('shows other channels alongside the email form', async () => {
		server.use(
			channelHandler([
				channel,
				{
					id: 'c2',
					kind: 'webhook',
					name: 'Deploy hook',
					enabled: true,
					url_preview: 'https://example.com/…',
					created_at: '2026-01-01T00:00:00Z',
					updated_at: '2026-01-01T00:00:00Z',
				},
			]),
		);

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		expect(
			await screen.findByRole('heading', { name: 'Other channels' }),
		).toBeInTheDocument();
		expect(screen.getByText('Deploy hook')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Add channel' }),
		).toBeInTheDocument();
		expect(screen.getByText('ops@example.com')).toBeInTheDocument();
	});

	test.each(['member', 'viewer'] as const)(
		'a %s sees a forbidden state',
		(role) => {
			renderWithProviders(
				<AlertSettingsScreen workspace={{ ...workspace, role }} />,
			);

			expect(
				screen.getByText('You do not have access to alert settings.'),
			).toBeInTheDocument();
			expect(
				screen.queryByRole('switch', { name: 'Email alerts' }),
			).not.toBeInTheDocument();
		},
	);

	test('saves the changed recipients in the PUT body and refreshes', async () => {
		const putBodies: unknown[] = [];
		const getCalls: string[] = [];
		let current: AlertChannel[] = [channel];
		server.use(
			http.get(channelsPath, () => {
				getCalls.push('get');
				return HttpResponse.json({ channels: current });
			}),
			http.put(emailChannelPath, async ({ request }) => {
				const body = (await request.json()) as {
					recipients: string[];
					enabled: boolean;
				};
				putBodies.push(body);
				current = [
					{
						...channel,
						recipients: body.recipients,
						enabled: body.enabled,
						updated_at: '2026-01-02T00:00:00Z',
					},
				];
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		await screen.findByText('ops@example.com');
		await user.type(
			screen.getByLabelText('Add recipient'),
			'dev@example.com{Enter}',
		);
		await user.click(
			screen.getByRole('button', { name: 'Remove ops@example.com' }),
		);
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(putBodies).toHaveLength(1));
		expect(putBodies[0]).toEqual({
			recipients: ['dev@example.com'],
			enabled: true,
		});
		expect(await screen.findByText('dev@example.com')).toBeInTheDocument();
		expect(screen.queryByText('ops@example.com')).not.toBeInTheDocument();
		await waitFor(() => expect(getCalls.length).toBeGreaterThanOrEqual(2));
		expect(toast.success).toHaveBeenCalledWith('Email alert settings saved');
	});

	test('includes a typed but unadded recipient when saving', async () => {
		const putBodies: { recipients: string[]; enabled: boolean }[] = [];
		let current: AlertChannel[] = [channel];
		server.use(
			http.get(channelsPath, () => HttpResponse.json({ channels: current })),
			http.put(emailChannelPath, async ({ request }) => {
				const body = (await request.json()) as {
					recipients: string[];
					enabled: boolean;
				};
				putBodies.push(body);
				current = [
					{
						...channel,
						recipients: body.recipients,
						enabled: body.enabled,
						updated_at: '2026-01-02T00:00:00Z',
					},
				];
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		await screen.findByText('ops@example.com');
		await user.type(screen.getByLabelText('Add recipient'), 'dev@example.com');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(putBodies).toHaveLength(1));
		expect(putBodies[0]).toEqual({
			recipients: ['ops@example.com', 'dev@example.com'],
			enabled: true,
		});
		expect(await screen.findByText('dev@example.com')).toBeInTheDocument();
	});

	test('an invalid typed draft blocks saving', async () => {
		const putBodies: unknown[] = [];
		server.use(
			channelHandler([channel]),
			http.put(emailChannelPath, async ({ request }) => {
				putBodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		await screen.findByText('ops@example.com');
		await user.type(screen.getByLabelText('Add recipient'), 'not-an-email');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		expect(
			await screen.findByText('Enter a valid email address'),
		).toBeInTheDocument();
		expect(putBodies).toHaveLength(0);
	});

	test('validates duplicates, invalid addresses, and an empty enabled list', async () => {
		const putBodies: unknown[] = [];
		server.use(
			channelHandler([channel]),
			http.put(emailChannelPath, async ({ request }) => {
				putBodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		await screen.findByText('ops@example.com');
		const input = screen.getByLabelText('Add recipient');

		await user.type(input, 'OPS@example.com{Enter}');
		expect(
			await screen.findByText('This recipient is already added'),
		).toBeInTheDocument();

		await user.clear(input);
		await user.type(input, 'not-an-email{Enter}');
		expect(
			await screen.findByText('Enter a valid email address'),
		).toBeInTheDocument();

		await user.clear(input);
		await user.click(
			screen.getByRole('button', { name: 'Remove ops@example.com' }),
		);
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		expect(
			await screen.findByText(
				'Add at least one recipient before enabling email alerts.',
			),
		).toBeInTheDocument();
		expect(putBodies).toHaveLength(0);
	});

	test('allows an empty list when email alerts are disabled', async () => {
		const putBodies: unknown[] = [];
		server.use(
			channelHandler([
				{
					...channel,
					enabled: false,
					recipients: [],
				},
			]),
			http.put(emailChannelPath, async ({ request }) => {
				putBodies.push(await request.json());
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		expect(
			await screen.findByRole('switch', { name: 'Email alerts' }),
		).not.toBeChecked();
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(putBodies).toHaveLength(1));
		expect(putBodies[0]).toEqual({ recipients: [], enabled: false });
	});

	test('shows a ProblemAlert when the save fails', async () => {
		server.use(
			channelHandler([channel]),
			http.put(emailChannelPath, () =>
				problem(
					'validation_error',
					400,
					'Validation failed',
					'recipient addresses are invalid',
				),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<AlertSettingsScreen workspace={workspace} />);

		await screen.findByText('ops@example.com');
		await user.type(
			screen.getByLabelText('Add recipient'),
			'dev@example.com{Enter}',
		);
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		expect(
			await screen.findByText('recipient addresses are invalid'),
		).toBeInTheDocument();
		expect(screen.getByText('Code: validation_error')).toBeInTheDocument();
		expect(toast.success).not.toHaveBeenCalled();
	});
});
