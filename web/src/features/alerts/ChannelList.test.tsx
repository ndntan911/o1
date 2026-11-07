import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { AlertChannel } from '@/lib/api/types';
import { ChannelList } from './ChannelList';

const ORIGIN = 'http://localhost:5173';
const channelsPath = `${ORIGIN}/api/v1/workspaces/w1/alert-channels`;

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const channels: AlertChannel[] = [
	{
		id: 'c2',
		kind: 'webhook',
		name: 'Deploy hook',
		enabled: true,
		url_preview: 'https://example.com/…',
		signing_secret_set: true,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
	},
	{
		id: 'c3',
		kind: 'slack',
		name: 'Slack #ops',
		enabled: false,
		url_preview: 'https://hooks.slack.com/…',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
	},
	{
		id: 'c4',
		kind: 'telegram',
		name: 'Telegram on-call',
		enabled: true,
		chat_id: '-1001234567890',
		bot_token_set: true,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
	},
];

function problem(code: string, status: number, title: string, detail?: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code, detail },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

describe('ChannelList', () => {
	beforeEach(() => {
		toast.success.mockReset();
	});

	test('renders non-email channels with masked targets and switches', () => {
		renderWithProviders(<ChannelList workspaceId="w1" channels={channels} />);

		expect(
			screen.getByRole('heading', { name: 'Other channels' }),
		).toBeInTheDocument();
		expect(screen.getByText('Deploy hook')).toBeInTheDocument();
		expect(screen.getByText('https://example.com/…')).toBeInTheDocument();
		expect(screen.getByText('Slack #ops')).toBeInTheDocument();
		expect(screen.getByText('https://hooks.slack.com/…')).toBeInTheDocument();
		expect(screen.getByText('-1001234567890')).toBeInTheDocument();
		expect(
			screen.getByRole('switch', { name: 'Deploy hook enabled' }),
		).toBeChecked();
		expect(
			screen.getByRole('switch', { name: 'Slack #ops enabled' }),
		).not.toBeChecked();
		expect(screen.queryByText('Email')).not.toBeInTheDocument();
		expect(document.body.textContent).not.toContain('0123456789abcdef');
		expect(document.body.textContent).not.toContain('123456:ABC');
	});

	test('Add channel opens the create dialog', async () => {
		renderWithProviders(<ChannelList workspaceId="w1" channels={[]} />);
		const user = userEvent.setup();

		expect(screen.getByText('No other channels yet.')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Add channel' }));

		expect(
			await screen.findByRole('heading', { name: 'Add alert channel' }),
		).toBeInTheDocument();
	});

	test('Test sends a test notification and shows a success toast', async () => {
		const calls: string[] = [];
		server.use(
			http.post(`${channelsPath}/c2/test`, () => {
				calls.push('test');
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<ChannelList workspaceId="w1" channels={channels} />);
		await user.click(screen.getByRole('button', { name: 'Test Deploy hook' }));

		await waitFor(() => expect(calls).toEqual(['test']));
		expect(toast.success).toHaveBeenCalledWith('Test notification sent');
	});

	test('a failed test send shows the sanitized problem', async () => {
		server.use(
			http.post(`${channelsPath}/c2/test`, () =>
				problem(
					'test_send_failed',
					422,
					'Test Send Failed',
					'webhook returned HTTP 500',
				),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<ChannelList workspaceId="w1" channels={channels} />);
		await user.click(screen.getByRole('button', { name: 'Test Deploy hook' }));

		expect(
			await screen.findByText('webhook returned HTTP 500'),
		).toBeInTheDocument();
		expect(screen.getByText('Code: test_send_failed')).toBeInTheDocument();
		expect(toast.success).not.toHaveBeenCalled();
	});

	test('Delete confirms with retained history and removes the channel', async () => {
		const deletions: string[] = [];
		server.use(
			http.delete(`${channelsPath}/c2`, () => {
				deletions.push('c2');
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<ChannelList workspaceId="w1" channels={channels} />);
		await user.click(
			screen.getByRole('button', { name: 'Delete Deploy hook' }),
		);

		expect(
			await screen.findByText(/delivery history is retained/i),
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Delete channel' }));

		await waitFor(() => expect(deletions).toEqual(['c2']));
		expect(toast.success).toHaveBeenCalledWith('Alert channel deleted');
	});
});
