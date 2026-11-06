import type { Incident, IncidentEvent } from '@/lib/api/types';

export type IncidentStatusVariant =
	'default' | 'destructive' | 'secondary' | 'outline';

const STATUS_LABELS: Record<Incident['status'], string> = {
	open: 'Open',
	acknowledged: 'Acknowledged',
	resolved: 'Resolved',
};

const STATUS_VARIANTS: Record<Incident['status'], IncidentStatusVariant> = {
	open: 'destructive',
	acknowledged: 'secondary',
	resolved: 'outline',
};

const EVENT_LABELS: Record<IncidentEvent['kind'], string> = {
	opened: 'Opened',
	acknowledged: 'Acknowledged',
	resolved: 'Resolved',
};

export function incidentStatusLabel(status: Incident['status']): string {
	return STATUS_LABELS[status];
}

export function incidentStatusVariant(
	status: Incident['status'],
): IncidentStatusVariant {
	return STATUS_VARIANTS[status];
}

export function incidentEventLabel(kind: IncidentEvent['kind']): string {
	return EVENT_LABELS[kind];
}

export function formatIncidentDuration(
	startedAt: string,
	endedAt: string | null,
	now: Date = new Date(),
): string {
	const start = new Date(startedAt).getTime();
	const end = endedAt ? new Date(endedAt).getTime() : now.getTime();
	const totalMinutes = Math.max(0, Math.floor((end - start) / 60_000));
	if (totalMinutes < 1) return '<1m';

	const days = Math.floor(totalMinutes / 1440);
	const hours = Math.floor((totalMinutes % 1440) / 60);
	const minutes = totalMinutes % 60;

	if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
	if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
	return `${minutes}m`;
}
