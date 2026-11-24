import type { ReactNode } from 'react';
import { isApiProblem } from '@/lib/api/client';
import type {
	IncidentInvestigation,
	InsightConfidence,
	InvestigationHypothesis,
} from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	useIncidentInvestigation,
	useStartIncidentInvestigation,
} from './investigationHooks';

const CONFIDENCE_LABELS: Record<InsightConfidence, string> = {
	high: 'High confidence',
	medium: 'Medium confidence',
	low: 'Low confidence',
};

const FAILURE_MESSAGES: Record<string, string> = {
	limit_exceeded:
		'The investigation hit its time or tool limit before it could finish.',
	provider_rejected: 'The model provider rejected the investigation.',
	provider_unavailable: 'The model provider was unavailable.',
	invalid_output: "The model's answer could not be read.",
	actor_removed:
		'The investigation stopped because its author left the workspace.',
	internal: 'The investigation could not be completed.',
};

export interface IncidentInvestigationPanelProps {
	workspaceId: string;
	incidentId: string;
	canStart: boolean;
}

/**
 * Shows the latest evidence-gathering investigation of an incident, and lets a
 * member start one. While a run is queued or executing the panel polls for
 * progress; it stops as soon as the run reaches a terminal state.
 */
export function IncidentInvestigationPanel({
	workspaceId,
	incidentId,
	canStart,
}: IncidentInvestigationPanelProps) {
	const investigationQuery = useIncidentInvestigation(
		workspaceId,
		incidentId,
	);
	const start = useStartIncidentInvestigation(workspaceId, incidentId);

	const missing =
		investigationQuery.isError &&
		isApiProblem(investigationQuery.error) &&
		investigationQuery.error.problem.status === 404;
	const disabled =
		investigationQuery.isError &&
		isApiProblem(investigationQuery.error) &&
		investigationQuery.error.problem.status === 501;
	const unavailable =
		(investigationQuery.isError && !missing && !disabled) || disabled;

	if (unavailable) {
		return null;
	}

	if (investigationQuery.isPending) {
		return null;
	}

	const investigation = investigationQuery.data;
	if (!investigation) {
		// A viewer with nothing stored has nothing to see; the panel stays out
		// of the way rather than offering an action they cannot take.
		if (!canStart) {
			return null;
		}
		return (
			<PanelFrame>
				<p className="text-sm text-muted-foreground">
					No investigation has run for this incident yet.
				</p>
				<StartButton
					busy={start.isPending}
					label="Investigate"
					onClick={() => start.mutate()}
				/>
				{start.isError ? <StartError /> : null}
			</PanelFrame>
		);
	}

	const active =
		investigation.status === 'queued' || investigation.status === 'running';
	const terminal =
		investigation.status === 'ok' ||
		investigation.status === 'insufficient';
	const failed =
		investigation.status === 'failed' ||
		investigation.status === 'incomplete';

	return (
		<PanelFrame
			action={
				canStart && terminal ? (
					<StartButton
						busy={start.isPending}
						label="Investigate again"
						onClick={() => start.mutate()}
					/>
				) : null
			}
		>
			{start.isError ? <StartError /> : null}

			{active ? <RunningBody investigation={investigation} /> : null}

			{terminal ? <ResultBody investigation={investigation} /> : null}

			{failed ? (
				<FailureBody
					investigation={investigation}
					canStart={canStart}
					busy={start.isPending}
					onRetry={() => start.mutate()}
				/>
			) : null}
		</PanelFrame>
	);
}

function PanelFrame({
	action,
	children,
}: {
	action?: ReactNode;
	children: ReactNode;
}) {
	return (
		<div className="space-y-3 rounded-lg border px-4 py-4">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<div>
					<h3 className="text-sm font-semibold">Investigate this incident</h3>
					<p className="text-xs text-muted-foreground">
						An agent reads this incident&apos;s own data and cites what it finds.
						Treat the result as a starting point, not a verdict.
					</p>
				</div>
				{action}
			</div>
			{children}
		</div>
	);
}

function StartButton({
	label,
	busy,
	onClick,
}: {
	label: string;
	busy: boolean;
	onClick: () => void;
}) {
	return (
		<Button type="button" size="sm" disabled={busy} onClick={onClick}>
			{busy ? 'Starting…' : label}
		</Button>
	);
}

function StartError() {
	return (
		<p className="text-sm text-destructive">
			The investigation could not be started. Try again in a moment.
		</p>
	);
}

function RunningBody({
	investigation,
}: {
	investigation: IncidentInvestigation;
}) {
	const steps = investigation.trace?.steps ?? [];

	return (
		<div role="status" aria-live="polite" className="space-y-2">
			<p className="text-sm">
				{investigation.status === 'queued'
					? 'Waiting to start…'
					: 'Gathering evidence…'}
				<span className="ml-2 text-xs text-muted-foreground">
					Elapsed {elapsedLabel(investigation)} · attempt{' '}
					{Math.max(1, investigation.attempts)}
				</span>
			</p>
			{steps.length > 0 ? (
				<ol className="space-y-1 text-xs text-muted-foreground">
					{steps.map((step) => (
						<li key={step.index}>
							{step.index + 1}. {step.tool ?? 'Model'} · {step.status}
							{step.args_summary ? ` — ${step.args_summary}` : ''}
							<span className="ml-1">{step.duration_ms} ms</span>
						</li>
					))}
				</ol>
			) : null}
		</div>
	);
}

function ResultBody({
	investigation,
}: {
	investigation: IncidentInvestigation;
}) {
	const result = investigation.result;
	const insufficient = investigation.status === 'insufficient';
	// Unverified causes are not offered as the answer: the refusal stands on
	// its own and the unsupported hypotheses stay out of the panel.
	const hypotheses = insufficient
		? []
		: (result?.hypotheses ?? []).slice(0, 3);
	const nextChecks = result?.next_checks ?? [];

	return (
		<div className="space-y-3">
			{result?.assessment ? (
				<p className="text-sm">{result.assessment}</p>
			) : (
				<p className="text-sm text-muted-foreground">
					The investigation finished without storing an assessment.
				</p>
			)}

			{insufficient ? (
				<p className="text-xs text-muted-foreground">
					Not enough evidence was collected to name a cause.
				</p>
			) : null}

			{hypotheses.length > 0 ? (
				<ol className="space-y-3">
					{hypotheses.map((hypothesis) => (
						<HypothesisItem
							key={hypothesis.rank}
							hypothesis={hypothesis}
						/>
					))}
				</ol>
			) : !insufficient ? (
				<p className="text-xs text-muted-foreground">
					No cause could be named from the gathered evidence.
				</p>
			) : null}

			{nextChecks.length > 0 ? (
				<div className="space-y-1">
					<h4 className="text-xs font-medium">Next checks</h4>
					<ul className="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
						{nextChecks.map((check) => (
							<li key={check}>{check}</li>
						))}
					</ul>
				</div>
			) : null}

			<p className="text-xs text-muted-foreground">
				{investigation.model} ·{' '}
				{formatRelativeTime(
					investigation.completed_at ?? investigation.created_at,
				)}
			</p>
		</div>
	);
}

function HypothesisItem({
	hypothesis,
}: {
	hypothesis: InvestigationHypothesis;
}) {
	const evidence = hypothesis.evidence ?? [];
	const unverified = hypothesis.unverified || evidence.length === 0;

	return (
		<li className="rounded-md border px-3 py-2">
			<div className="flex flex-wrap items-center gap-2">
				<Badge variant="outline">#{hypothesis.rank}</Badge>
				<span className="text-sm font-medium">{hypothesis.cause}</span>
				<Badge variant="secondary">
					{CONFIDENCE_LABELS[hypothesis.confidence] ?? hypothesis.confidence}
				</Badge>
				{unverified ? <Badge variant="destructive">Unverified</Badge> : null}
			</div>

			{evidence.length > 0 ? (
				<ul className="mt-2 flex flex-wrap gap-1">
					{evidence.map((item, index) => (
						<li key={`${hypothesis.rank}-${item.tool_call_id}-${index}`}>
							<Badge variant="outline" className="font-mono font-normal">
								{item.pointer || item.tool_call_id}: {evidenceValue(item.value)}
							</Badge>
						</li>
					))}
				</ul>
			) : null}

			{hypothesis.falsified_by ? (
				<p className="mt-2 text-xs text-muted-foreground">
					Would be wrong if: {hypothesis.falsified_by}
				</p>
			) : null}
		</li>
	);
}

function FailureBody({
	investigation,
	canStart,
	busy,
	onRetry,
}: {
	investigation: IncidentInvestigation;
	canStart: boolean;
	busy: boolean;
	onRetry: () => void;
}) {
	return (
		<div className="space-y-2">
			<p className="text-sm text-destructive">
				{FAILURE_MESSAGES[investigation.error_code] ??
					'The investigation could not be completed.'}
			</p>
			{investigation.error_code ? (
				<p className="text-xs text-muted-foreground">
					Error code: {investigation.error_code}
				</p>
			) : null}
			{canStart ? (
				<Button
					type="button"
					variant="outline"
					size="sm"
					disabled={busy}
					onClick={onRetry}
				>
					{busy ? 'Retrying…' : 'Retry'}
				</Button>
			) : null}
		</div>
	);
}

function elapsedLabel(investigation: IncidentInvestigation): string {
	const start = new Date(investigation.created_at).getTime();
	const end = investigation.completed_at
		? new Date(investigation.completed_at).getTime()
		: Date.now();
	const seconds = Math.max(0, Math.round((end - start) / 1_000));
	if (seconds < 60) {
		return `${seconds}s`;
	}
	const minutes = Math.floor(seconds / 60);
	const remainder = seconds % 60;
	return remainder > 0 ? `${minutes}m ${remainder}s` : `${minutes}m`;
}

function evidenceValue(value: unknown): string {
	if (value === null || value === undefined) {
		return 'null';
	}
	if (typeof value === 'string') {
		return truncate(value);
	}
	if (typeof value === 'number' || typeof value === 'boolean') {
		return String(value);
	}
	try {
		return truncate(JSON.stringify(value));
	} catch {
		return String(value);
	}
}

function truncate(text: string, limit = 80): string {
	return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}
