import { describe, expect, test } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { IncidentInvestigation } from '@/lib/api/types';
import { IncidentInvestigationPanel } from './IncidentInvestigationPanel';

const workspaceId = '11111111-1111-1111-1111-111111111111';
const incidentId = '22222222-2222-2222-2222-222222222222';

function investigation(
	overrides: Partial<IncidentInvestigation> = {},
): IncidentInvestigation {
	return {
		id: '55555555-5555-5555-5555-555555555555',
		incident_id: incidentId,
		status: 'ok',
		model: 'stub',
		result: {
			sufficient: true,
			assessment:
				'The target stopped answering because checks hit timeouts above the limit.',
			hypotheses: [
				{
					rank: 1,
					cause: 'Timeouts above the 10s limit',
					confidence: 'high',
					evidence: [
						{
							tool_call_id: 'call-1',
							pointer: '/data/checks/0/latency_ms',
							value: 12_000,
							note: 'above the limit',
						},
					],
					falsified_by: 'A successful check in the same window',
				},
				{
					rank: 2,
					cause: 'Certificate expiry',
					confidence: 'medium',
					evidence: [],
					unverified: true,
				},
				{
					rank: 3,
					cause: 'DNS resolution failure',
					confidence: 'low',
					evidence: [
						{
							tool_call_id: 'call-2',
							pointer: '/data/checks/1/error',
							value: 'dns: no such host',
						},
					],
				},
				{
					rank: 4,
					cause: 'A fourth cause that must stay hidden',
					confidence: 'low',
					evidence: [
						{
							tool_call_id: 'call-3',
							pointer: '/data/checks/2/error',
							value: 'timeout',
						},
					],
				},
			],
			next_checks: ['Check the TLS certificate expiry date'],
		},
		trace: { steps: [] },
		attempts: 1,
		tool_calls: 2,
		input_tokens: 10,
		output_tokens: 20,
		error_code: '',
		created_at: '2026-11-20T09:30:00Z',
		completed_at: '2026-11-20T09:31:00Z',
		...overrides,
	};
}

function running(overrides: Partial<IncidentInvestigation> = {}) {
	return investigation({
		status: 'running',
		result: null,
		attempts: 1,
		completed_at: null,
		created_at: new Date(Date.now() - 3_000).toISOString(),
		trace: {
			steps: [
				{
					index: 0,
					status: 'ok',
					duration_ms: 12,
				},
				{
					index: 1,
					tool: 'checks.list',
					args_summary: 'window=1h',
					status: 'ok',
					duration_ms: 40,
				},
			],
		},
		...overrides,
	});
}

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

const base = `/api/v1/workspaces/${workspaceId}/incidents/${incidentId}/investigations`;
const latestPath = `${base}/latest`;

describe('IncidentInvestigationPanel', () => {
	test(
		'offers to investigate, shows the running state, then the stored result',
		async () => {
			let phase: 'missing' | 'running' | 'ok' = 'missing';

			server.use(
				http.get(latestPath, () => {
					if (phase === 'missing') {
						return new HttpResponse(null, { status: 404 });
					}
					if (phase === 'running') {
						return HttpResponse.json(running());
					}
					return HttpResponse.json(investigation());
				}),
				http.post(
					base,
					() => {
						phase = 'running';
						return HttpResponse.json(running(), { status: 202 });
					},
				),
			);

			renderWithProviders(
				<IncidentInvestigationPanel
					workspaceId={workspaceId}
					incidentId={incidentId}
					canStart
				/>,
			);

			await userEvent.click(
				await screen.findByRole('button', { name: /^investigate$/i }),
			);

			const runningRegion = await screen.findByRole('status');
			expect(runningRegion).toHaveAttribute('aria-live', 'polite');
			expect(runningRegion).toHaveTextContent(/checks\.list/i);
			expect(runningRegion).toHaveTextContent(/attempt 1/i);

			phase = 'ok';

			expect(
				await screen.findByText(/timeouts above the limit/i, undefined, {
					timeout: 5_000,
				}),
			).toBeInTheDocument();
			expect(screen.getByText(/unverified/i)).toBeInTheDocument();
			expect(screen.getByText(/latency_ms/i)).toBeInTheDocument();
			expect(screen.getByText(/high confidence/i)).toBeInTheDocument();
			expect(
				screen.getByText(/check the tls certificate expiry date/i),
			).toBeInTheDocument();
			expect(
				screen.queryByText(/a fourth cause that must stay hidden/i),
			).not.toBeInTheDocument();
		},
		10_000,
	);

	test('hides the action from viewers but still shows a stored result', async () => {
		server.use(http.get(latestPath, () => HttpResponse.json(investigation())));

		renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart={false}
			/>,
		);

		expect(
			await screen.findByText(/timeouts above the limit/i),
		).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: /investigate/i }),
		).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument();
	});

	test('shows a retry after a failed run', async () => {
		let phase: 'failed' | 'running' = 'failed';
		let posts = 0;

		server.use(
			http.get(latestPath, () =>
				phase === 'failed'
					? HttpResponse.json(
							investigation({
								status: 'failed',
								result: null,
								error_code: 'provider_rejected',
							}),
						)
					: HttpResponse.json(running()),
			),
			http.post(base, () => {
				posts += 1;
				phase = 'running';
				return HttpResponse.json(running(), { status: 202 });
			}),
		);

		renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart
			/>,
		);

		expect(await screen.findByText(/provider rejected/i)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));

		expect(await screen.findByRole('status')).toBeInTheDocument();
		expect(posts).toBe(1);
	});

	test('seeds the active row returned by a 409 conflict', async () => {
		let posts = 0;

		server.use(
			http.get(latestPath, () => new HttpResponse(null, { status: 404 })),
			http.post(base, () => {
				posts += 1;
				return HttpResponse.json(running({ attempts: 2 }), { status: 409 });
			}),
		);

		renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart
			/>,
		);

		await userEvent.click(
			await screen.findByRole('button', { name: /^investigate$/i }),
		);

		expect(await screen.findByRole('status')).toHaveTextContent(/attempt 2/i);
		expect(posts).toBe(1);
	});

	test('does not treat a 409 problem as a running investigation', async () => {
		server.use(
			http.get(latestPath, () => new HttpResponse(null, { status: 404 })),
			http.post(base, () =>
				problem(
					'quota_exceeded',
					409,
					'Investigation Already Running',
				),
			),
		);

		renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart
			/>,
		);

		await userEvent.click(
			await screen.findByRole('button', { name: /^investigate$/i }),
		);

		expect(
			await screen.findByText(/could not be started/i),
		).toBeInTheDocument();
		expect(screen.queryByRole('status')).not.toBeInTheDocument();
	});

	test('shows the refusal when every hypothesis was unverified', async () => {
		server.use(
			http.get(latestPath, () =>
				HttpResponse.json(
					investigation({
						status: 'insufficient',
						result: {
							sufficient: false,
							assessment: 'The evidence is too thin to name a cause.',
							hypotheses: [
								{
									rank: 1,
									cause: 'Certificate expiry',
									confidence: 'medium',
									evidence: [],
									unverified: true,
								},
							],
							next_checks: ['Collect checks from another region'],
						},
					}),
				),
			),
		);

		renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart
			/>,
		);

		expect(
			await screen.findByText(/too thin to name a cause/i),
		).toBeInTheDocument();
		expect(
			screen.queryByText('Certificate expiry'),
		).not.toBeInTheDocument();
	});

	test('renders nothing when investigations are not configured', async () => {
		let calls = 0;

		server.use(
			http.get(latestPath, () => {
				calls += 1;
				return problem('not_implemented', 501, 'Copilot is not configured');
			}),
		);

		const { container } = renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart
			/>,
		);

		await waitFor(() => expect(calls).toBe(1));
		expect(container).toBeEmptyDOMElement();
	});

	test('renders nothing for a viewer when nothing is stored', async () => {
		let calls = 0;

		server.use(
			http.get(latestPath, () => {
				calls += 1;
				return new HttpResponse(null, { status: 404 });
			}),
		);

		const { container } = renderWithProviders(
			<IncidentInvestigationPanel
				workspaceId={workspaceId}
				incidentId={incidentId}
				canStart={false}
			/>,
		);

		await waitFor(() => expect(calls).toBe(1));
		expect(container).toBeEmptyDOMElement();
	});
});
