import { expect, test } from 'vitest';
import { screen } from '@testing-library/react';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { PublicStatusPage, PublicStatusMonitor } from '@/lib/api/types';
import { PublicStatusScreen, overallBanner } from './PublicStatusScreen';

const ORIGIN = 'http://localhost:5173';

function days(downIndexes: number[] = []) {
	return Array.from({ length: 90 }, (_, index) => ({
		date: new Date(Date.UTC(2026, 7, 11 + index)).toISOString().slice(0, 10),
		status: downIndexes.includes(index) ? ('down' as const) : ('up' as const),
		uptime: downIndexes.includes(index) ? 0 : 1,
	}));
}

function monitor(
	overrides: Partial<PublicStatusMonitor> = {},
): PublicStatusMonitor {
	return {
		id: 'm1',
		name: 'API',
		status: 'up',
		uptime: 0.999,
		days: days(),
		...overrides,
	};
}

function page(overrides: Partial<PublicStatusPage> = {}): PublicStatusPage {
	return {
		workspace: { name: 'Acme', slug: 'acme' },
		page: {
			title: 'Acme status',
			description: 'Live availability',
			updated_at: '2026-11-08T09:00:00Z',
		},
		monitors: [monitor()],
		incidents: { active: [], recent: [] },
		...overrides,
	};
}

function serve(payload: PublicStatusPage) {
	server.use(
		http.get(`${ORIGIN}/api/v1/public/status/acme`, () =>
			HttpResponse.json(payload),
		),
	);
}

test('renders the page, monitor, 90 bars, and uptime', async () => {
	serve(page());
	renderWithProviders(<PublicStatusScreen slug="acme" />);

	expect(
		await screen.findByRole('heading', { name: 'Acme status' }),
	).toBeInTheDocument();
	expect(screen.getByText('Live availability')).toBeInTheDocument();
	expect(screen.getByText('API')).toBeInTheDocument();
	expect(screen.getAllByTestId('status-day')).toHaveLength(90);
	expect(
		screen.getByText('99.90% uptime over the last 90 days'),
	).toBeInTheDocument();
});

test('encodes day status with a non-color cue', async () => {
	serve(
		page({
			monitors: [
				monitor({
					days: [
						{ date: '2026-11-08', status: 'up', uptime: 1 },
						{ date: '2026-11-07', status: 'partial', uptime: 0.5 },
						{ date: '2026-11-06', status: 'down', uptime: 0 },
						{ date: '2026-11-05', status: 'no_data', uptime: null },
					],
				}),
			],
		}),
	);
	renderWithProviders(<PublicStatusScreen slug="acme" />);

	await screen.findByText('API');
	const bars = screen.getAllByTestId('status-day');
	expect(bars).toHaveLength(4);

	expect(bars[0]).toHaveAttribute('data-status', 'up');
	expect(bars[0]).toHaveClass('border-solid');

	expect(bars[1]).toHaveAttribute('data-status', 'partial');
	expect(bars[1]).toHaveClass('border-dashed');

	expect(bars[2]).toHaveAttribute('data-status', 'down');
	expect(bars[2]).toHaveClass('border-2');

	expect(bars[3]).toHaveAttribute('data-status', 'no_data');
	expect(bars[3]).toHaveClass('border-dotted');
});

test('shows no data instead of a percentage', async () => {
	serve(
		page({
			monitors: [
				monitor({
					uptime: null,
					days: days().map((day) => ({
						...day,
						status: 'no_data',
						uptime: null,
					})),
				}),
			],
		}),
	);
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(
		await screen.findByText('No data over the last 90 days'),
	).toBeInTheDocument();
});

test('renders active and recent incidents without acknowledgement identity', async () => {
	serve(
		page({
			incidents: {
				active: [
					{
						id: 'i1',
						monitor_id: 'm1',
						monitor_name: 'API',
						status: 'ongoing',
						started_at: '2026-11-08T08:00:00Z',
						resolved_at: null,
					},
				],
				recent: [
					{
						id: 'i2',
						monitor_id: 'm1',
						monitor_name: 'API',
						status: 'resolved',
						started_at: '2026-11-07T08:00:00Z',
						resolved_at: '2026-11-07T08:20:00Z',
					},
				],
			},
		}),
	);
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(await screen.findByText('Active incidents')).toBeInTheDocument();
	expect(screen.getByText('Past incidents')).toBeInTheDocument();
	expect(screen.getAllByText('API').length).toBeGreaterThan(0);
	expect(screen.queryByText(/acknowledged/i)).not.toBeInTheDocument();
});

test('renders the not-found state for a 404', async () => {
	server.use(
		http.get(`${ORIGIN}/api/v1/public/status/missing`, () =>
			HttpResponse.json(
				{
					type: 'about:blank',
					title: 'Not Found',
					status: 404,
					code: 'not_found',
				},
				{
					status: 404,
					headers: { 'Content-Type': 'application/problem+json' },
				},
			),
		),
	);
	renderWithProviders(<PublicStatusScreen slug="missing" />);
	expect(await screen.findByText('Status page not found')).toBeInTheDocument();
});

test('banner precedence', () => {
	expect(overallBanner([]).tone).toBe('none');
	expect(overallBanner([monitor()]).text).toBe('All systems operational');
	expect(overallBanner([monitor({ status: 'paused' })]).text).toBe(
		'Status updates are paused',
	);
	expect(
		overallBanner([monitor(), monitor({ id: 'm2', status: 'paused' })]).text,
	).toBe('All systems operational');
	expect(
		overallBanner([monitor(), monitor({ id: 'm2', status: 'down' })]).text,
	).toBe('Some systems are experiencing issues');
});

test('never renders a monitor URL even if an extra field is present', async () => {
	const payload = page({
		monitors: [
			{
				...monitor(),
				url: 'https://api.example.com/health',
			} as PublicStatusMonitor,
		],
	});
	serve(payload);
	const { container } = renderWithProviders(<PublicStatusScreen slug="acme" />);
	expect(await screen.findByText('API')).toBeInTheDocument();
	expect(container.textContent).not.toContain('api.example.com');
});

test('sets the document title', async () => {
	serve(page());
	renderWithProviders(<PublicStatusScreen slug="acme" />);
	await screen.findByRole('heading', { name: 'Acme status' });
	expect(document.title).toBe('Acme status · o1 uptime');
});
