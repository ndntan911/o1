import { isApiProblem } from '@/lib/api/client';
import type {
	IncidentEvent,
	IncidentInsight,
	InsightHypothesis,
} from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { useGenerateIncidentInsight, useIncidentInsight } from './insightHooks';

const CONFIDENCE_LABELS: Record<InsightHypothesis['confidence'], string> = {
	high: 'High confidence',
	medium: 'Medium confidence',
	low: 'Low confidence',
};

export interface IncidentInsightPanelProps {
	workspaceId: string;
	incidentId: string;
	events: IncidentEvent[];
}

/**
 * Shows a stored analysis of the incident, and lets the reader ask for one.
 * Everything the model said is presented as a hypothesis, never as fact: each
 * one carries the evidence it rests on, and any claim it could not tie to the
 * incident's own data is labelled unverified.
 */
export function IncidentInsightPanel({
	workspaceId,
	incidentId,
	events,
}: IncidentInsightPanelProps) {
	const insightQuery = useIncidentInsight(workspaceId, incidentId);
	const generate = useGenerateIncidentInsight(workspaceId, incidentId);

	const insight = insightQuery.data;
	const missing =
		insightQuery.isError &&
		isApiProblem(insightQuery.error) &&
		insightQuery.error.problem.status === 404;
	const disabled =
		insightQuery.isError &&
		isApiProblem(insightQuery.error) &&
		insightQuery.error.problem.status === 501;
	const unavailable = (insightQuery.isError && !missing && !disabled) || disabled;
	const busy = generate.isPending;
	const eventLabel = (eventId?: string) => {
		if (!eventId) {
			return null;
		}
		const match = events.find((event) => event.id === eventId);
		return match ? match.kind : null;
	};

	if (unavailable) {
		return null;
	}

	return (
		<div className="space-y-3 rounded-lg border px-4 py-4">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<div>
					<h3 className="text-sm font-semibold">Explain this incident</h3>
					<p className="text-xs text-muted-foreground">
						Hypotheses drawn only from this incident&apos;s own data. Treat them
						as a starting point, not a verdict.
					</p>
				</div>
				{insight ? (
					<Button
						type="button"
						variant="outline"
						size="sm"
						disabled={busy}
						onClick={() => generate.mutate(true)}
					>
						{busy ? 'Analysing…' : 'Run again'}
					</Button>
				) : null}
			</div>

			{generate.isError ? (
				<p className="text-sm text-destructive">
					The analysis could not be generated. Try again in a moment.
				</p>
			) : null}

			{!insight && !busy ? (
				<Button
					type="button"
					size="sm"
					disabled={busy}
					onClick={() => generate.mutate(false)}
				>
					Explain this incident
				</Button>
			) : null}

			{busy && !insight ? (
				<p className="text-sm text-muted-foreground">Reading the incident…</p>
			) : null}

			{insight ? <InsightBody insight={insight} eventLabel={eventLabel} /> : null}
		</div>
	);
}

function InsightBody({
	insight,
	eventLabel,
}: {
	insight: IncidentInsight;
	eventLabel: (eventId?: string) => string | null;
}) {
	// The API guarantees an array, but a row stored before that contract
	// existed, or a proxy that rewrites JSON, must not take the page down.
	const hypotheses = insight.result.hypotheses ?? [];

	return (
		<div className="space-y-3">
			<p className="text-sm">{insight.result.assessment}</p>

			{insight.status === 'failed' ? (
				<p className="text-xs text-muted-foreground">
					Nothing was analysed. The text above explains why.
				</p>
			) : null}

			{hypotheses.length > 0 ? (
				<ol className="space-y-3">
					{hypotheses.map((hypothesis) => (
						<li key={hypothesis.rank} className="rounded-md border px-3 py-2">
							<div className="flex flex-wrap items-center gap-2">
								<Badge variant="outline">#{hypothesis.rank}</Badge>
								<span className="text-sm font-medium">{hypothesis.cause}</span>
								<Badge variant="secondary">
									{CONFIDENCE_LABELS[hypothesis.confidence]}
								</Badge>
								{hypothesis.unsupported ? (
									<Badge variant="destructive">Unverified</Badge>
								) : null}
							</div>

							{(hypothesis.evidence ?? []).length > 0 ? (
								<ul className="mt-2 space-y-1 text-xs text-muted-foreground">
									{(hypothesis.evidence ?? []).map((item, index) => (
										<li key={`${hypothesis.rank}-${index}`}>
											{item.kind === 'event' && item.event_id
												? `Timeline: ${eventLabel(item.event_id) ?? 'unknown event'}`
												: `Field: ${item.field ?? 'unknown'}`}
											{item.note ? ` — ${item.note}` : ''}
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
					))}
				</ol>
			) : (
				<p className="text-xs text-muted-foreground">
					There is not enough evidence in this incident to support a cause.
				</p>
			)}

			<p className="text-xs text-muted-foreground">
				{insight.model} · generated {formatRelativeTime(insight.created_at)}
			</p>
		</div>
	);
}
