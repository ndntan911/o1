import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { AlertChannel } from '@/lib/api/types';
import { ChannelDialog } from './ChannelDialog';

Element.prototype.hasPointerCapture = () => false;
Element.prototype.setPointerCapture = () => undefined;
Element.prototype.releasePointerCapture = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const ORIGIN = 'http://localhost:5173';
const channelsPath = `${ORIGIN}/api/v1/workspaces/w1/alert-channels`;

const toast = vi.hoisted(() => ({
	success: vi.fn(),
	error: vi.fn(),
}));

vi.mock('sonner', () => ({ toast }));

const editChannel: AlertChannel = {
	id: 'c2',
	kind: 'webhook',
	name: 'Deploy hook',
	enabled: true,
	url_preview: 'https://example.com/…',
	signing_secret_set: true,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

function problem(code: string, status: number, title: string, detail?: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code, detail },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

describe('ChannelDialog', () => {
	beforeEach(() => {
		toast.success.mockReset();
	});

	test('create mode swaps kind-specific fields', async () => {
		renderWithProviders(
			<ChannelDialog workspaceId="w1" channel={null} onClose={vi.fn()} />,
		);
		const user = userEvent.setup();

		expect(
			screen.getByRole('heading', { name: 'Add alert channel' }),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Webhook URL')).toBeInTheDocument();
		expect(screen.getByLabelText('Signing secret')).toBeInTheDocument();

		await user.click(screen.getByRole('combobox', { name: 'Kind' }));
		await user.click(await screen.findByRole('option', { name: 'Slack' }));
		expect(screen.getByLabelText('Slack webhook URL')).toBeInTheDocument();
		expect(screen.queryByLabelText('Webhook URL')).not.toBeInTheDocument();

		await user.click(screen.getByRole('combobox', { name: 'Kind' }));
		await user.click(await screen.findByRole('option', { name: 'Telegram' }));
		expect(screen.getByLabelText('Bot token')).toBeInTheDocument();
		expect(screen.getByLabelText('Chat ID')).toBeInTheDocument();
	});

	test('submits a webhook and closes on success', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(channelsPath, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json({ ...editChannel, id: 'c3' }, { status: 201 });
			}),
		);
		const onClose = vi.fn();
		const user = userEvent.setup();

		renderWithProviders(
			<ChannelDialog workspaceId="w1" channel={null} onClose={onClose} />,
		);

		await user.type(screen.getByLabelText('Name'), 'Deploy hook');
		await user.type(
			screen.getByLabelText('Webhook URL'),
			'https://example.com/o1',
		);
		await user.type(
			screen.getByLabelText('Signing secret'),
			'0123456789abcdef',
		);
		await user.click(screen.getByRole('button', { name: 'Save channel' }));

		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(bodies).toEqual([
			{
				kind: 'webhook',
				name: 'Deploy hook',
				enabled: true,
				url: 'https://example.com/o1',
				signing_secret: '0123456789abcdef',
			},
		]);
		expect(toast.success).toHaveBeenCalled();
	});

	test('edit mode keeps the kind and omits blank write-only values', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.patch(`${channelsPath}/${editChannel.id}`, async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(editChannel);
			}),
		);
		const onClose = vi.fn();
		const user = userEvent.setup();

		renderWithProviders(
			<ChannelDialog
				workspaceId="w1"
				channel={editChannel}
				onClose={onClose}
			/>,
		);

		expect(
			screen.getByRole('heading', { name: 'Edit alert channel' }),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Name')).toHaveValue('Deploy hook');
		expect(
			screen.queryByRole('combobox', { name: 'Kind' }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText('Configured — leave blank to keep'),
		).toBeInTheDocument();
		expect(screen.getByLabelText('Webhook URL')).toHaveValue('');

		await user.click(screen.getByRole('button', { name: 'Save channel' }));

		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(bodies).toEqual([{ name: 'Deploy hook', enabled: true }]);
	});

	test('a validation problem keeps the dialog open', async () => {
		server.use(
			http.patch(`${channelsPath}/${editChannel.id}`, () =>
				problem('validation_error', 400, 'Validation Error', 'url is required'),
			),
		);
		const onClose = vi.fn();
		const user = userEvent.setup();

		renderWithProviders(
			<ChannelDialog
				workspaceId="w1"
				channel={editChannel}
				onClose={onClose}
			/>,
		);

		await user.click(screen.getByRole('button', { name: 'Save channel' }));

		expect(await screen.findByText('url is required')).toBeInTheDocument();
		expect(
			screen.getByRole('heading', { name: 'Edit alert channel' }),
		).toBeInTheDocument();
		expect(onClose).not.toHaveBeenCalled();
	});
});
