import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { CreateMonitorInput, Monitor, Workspace } from '@/lib/api/types';
import { MonitorCreateScreen } from './MonitorCreateScreen';

const ORIGIN = 'http://localhost:5173';

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

const created: Monitor = {
	id: 'm1',
	workspace_id: workspace.id,
	name: 'API',
	url: 'https://api.example.com/health',
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
	status: 'unknown',
	last_check: null,
	summary_24h: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

const collectionPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/monitors`;

describe('MonitorCreateScreen', () => {
	beforeEach(() => {
		harness.navigate.mockReset();
		harness.navigate.mockResolvedValue(undefined);
	});

	test('creates a monitor with defaults and navigates to its detail page', async () => {
		const creates: CreateMonitorInput[] = [];
		server.use(
			http.post(collectionPath, async ({ request }) => {
				creates.push((await request.json()) as CreateMonitorInput);
				return HttpResponse.json(created, { status: 201 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorCreateScreen workspace={workspace} />);

		await user.type(screen.getByLabelText('Name'), 'API');
		await user.type(
			screen.getByLabelText('URL'),
			'https://api.example.com/health',
		);
		await user.click(screen.getByRole('button', { name: 'Create monitor' }));

		await waitFor(() => expect(creates).toHaveLength(1));

		expect(creates[0]).toEqual({
			name: 'API',
			url: 'https://api.example.com/health',
			method: 'GET',
			headers: {},
			expected_status: '2xx',
			interval_seconds: 60,
			timeout_seconds: 10,
			confirmation_threshold: 2,
			recovery_threshold: 2,
		});
		expect(creates[0]).not.toHaveProperty('body');
		expect(creates[0]).not.toHaveProperty('content_type');
		expect(harness.navigate).toHaveBeenCalledWith({
			href: `/w/${workspace.slug}/monitors/${created.id}`,
		});
	});

	test('shows a page-level alert when the workspace is suspended', async () => {
		server.use(
			http.post(collectionPath, () =>
				HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Workspace suspended',
						status: 403,
						code: 'workspace_suspended',
					},
					{
						status: 403,
						headers: { 'Content-Type': 'application/problem+json' },
					},
				),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorCreateScreen workspace={workspace} />);

		await user.type(screen.getByLabelText('Name'), 'API');
		await user.type(
			screen.getByLabelText('URL'),
			'https://api.example.com/health',
		);
		await user.click(screen.getByRole('button', { name: 'Create monitor' }));

		expect(await screen.findByText('Workspace suspended')).toBeInTheDocument();
		expect(harness.navigate).not.toHaveBeenCalled();
	});
});
