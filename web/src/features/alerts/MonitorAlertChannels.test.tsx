import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { MonitorChannel, Workspace } from '@/lib/api/types';
import { MonitorAlertChannels } from './MonitorAlertChannels';

vi.mock('@tanstack/react-router', () => ({
	Link: ({
		to,
		params,
		children,
		...props
	}: {
		to?: string;
		params?: Record<string, unknown>;
		children?: ReactNode;
	} & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'>) => {
		let href = to;
		for (const [key, value] of Object.entries(params ?? {})) {
			href = href?.replace(`$${key}`, String(value));
		}
		return (
			<a href={href} {...props}>
				{children}
			</a>
		);
	},
}));

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'member',
};

const path = `http://localhost:5173/api/v1/workspaces/w1/monitors/m1/alert-channels`;

const channels: MonitorChannel[] = [
	{
		id: 'c1',
		kind: 'email',
		name: 'Email',
		channel_enabled: true,
		enabled: true,
	},
	{
		id: 'c2',
		kind: 'webhook',
		name: 'Deploy hook',
		channel_enabled: true,
		enabled: true,
	},
];

function problem(code: string, status: number, title: string, detail?: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code, detail },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

function listHandler(list: MonitorChannel[]) {
	return http.get(path, () => HttpResponse.json({ channels: list }));
}

describe('MonitorAlertChannels', () => {
	test('renders one row per channel with kind labels and switches', async () => {
		server.use(listHandler(channels));

		renderWithProviders(
			<MonitorAlertChannels workspace={workspace} monitorId="m1" canManage />,
		);

		expect(
			await screen.findByRole('heading', { name: 'Alert channels' }),
		).toBeInTheDocument();
		expect(await screen.findByText('Email')).toBeInTheDocument();
		expect(await screen.findByText('Deploy hook')).toBeInTheDocument();
		expect(screen.getByRole('switch', { name: 'Email alerts' })).toBeChecked();
		expect(
			screen.getByRole('switch', { name: 'Deploy hook alerts' }),
		).toBeChecked();
	});

	test('toggling a channel sends only that channel id', async () => {
		const patches: Array<{ path: string; body: unknown }> = [];
		server.use(listHandler(channels));
		server.use(
			http.patch(`${path}/c2`, async ({ request }) => {
				patches.push({
					path: new URL(request.url).pathname,
					body: await request.json(),
				});
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorAlertChannels workspace={workspace} monitorId="m1" canManage />,
		);

		const toggle = await screen.findByRole('switch', {
			name: 'Deploy hook alerts',
		});
		await user.click(toggle);

		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0].path).toBe(
			'/api/v1/workspaces/w1/monitors/m1/alert-channels/c2',
		);
		expect(patches[0].body).toEqual({ enabled: false });
	});

	test('a globally disabled channel is disabled with a note', async () => {
		server.use(
			listHandler([
				channels[0],
				{ ...channels[1], channel_enabled: false, enabled: false },
			]),
		);

		renderWithProviders(
			<MonitorAlertChannels workspace={workspace} monitorId="m1" canManage />,
		);

		expect(
			await screen.findByText('Disabled for this workspace'),
		).toBeInTheDocument();
		expect(
			screen.getByRole('switch', { name: 'Deploy hook alerts' }),
		).toBeDisabled();
	});

	test('an empty list links admins to alert settings', async () => {
		server.use(listHandler([]));

		renderWithProviders(
			<MonitorAlertChannels
				workspace={{ ...workspace, role: 'admin' }}
				monitorId="m1"
				canManage
			/>,
		);

		expect(
			await screen.findByText(
				'No alert channels are configured for this workspace.',
			),
		).toBeInTheDocument();
		expect(
			screen.getByRole('link', { name: 'Configure alert channels' }),
		).toHaveAttribute('href', '/w/acme-status/settings/alerts');
	});

	test('a failed toggle shows a problem and restores the switch', async () => {
		server.use(listHandler(channels));
		server.use(
			http.patch(`${path}/c2`, () =>
				problem(
					'internal_error',
					500,
					'Internal Server Error',
					'Please retry.',
				),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorAlertChannels workspace={workspace} monitorId="m1" canManage />,
		);

		const toggle = await screen.findByRole('switch', {
			name: 'Deploy hook alerts',
		});
		await user.click(toggle);

		expect(await screen.findByText('Please retry.')).toBeInTheDocument();
		await waitFor(() => expect(toggle).toBeChecked());
	});

	test('viewers see nothing', () => {
		const { container } = renderWithProviders(
			<MonitorAlertChannels
				workspace={{ ...workspace, role: 'viewer' }}
				monitorId="m1"
				canManage={false}
			/>,
		);

		expect(container).toBeEmptyDOMElement();
	});
});
