import { Badge } from '@/components/ui/badge';
import type { IncidentEvent } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { incidentEventLabel } from './status';

export interface IncidentTimelineProps {
	events: IncidentEvent[];
}

export function IncidentTimeline({ events }: IncidentTimelineProps) {
	const ordered = [...events].sort(
		(left, right) =>
			new Date(left.created_at).getTime() -
			new Date(right.created_at).getTime(),
	);

	return (
		<ol aria-label="Incident timeline" className="space-y-3">
			{ordered.map((event) => (
				<li
					key={event.id}
					className="flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-sm"
				>
					<div className="flex items-center gap-2">
						<Badge variant="outline">{incidentEventLabel(event.kind)}</Badge>
						{event.actor_user_id ? (
							<span className="text-xs text-muted-foreground">
								By {event.actor_user_id}
							</span>
						) : null}
					</div>
					<time
						dateTime={event.created_at}
						className="text-xs text-muted-foreground"
					>
						{formatRelativeTime(event.created_at)}
					</time>
				</li>
			))}
		</ol>
	);
}
