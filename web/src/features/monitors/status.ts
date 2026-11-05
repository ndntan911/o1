import type { MonitorStatus } from '@/lib/api/types';

export type StatusVariant = 'default' | 'destructive' | 'secondary' | 'outline';

const LABELS: Record<MonitorStatus, string> = {
	up: 'Up',
	down: 'Down',
	unknown: 'Unknown',
	paused: 'Paused',
};

const VARIANTS: Record<MonitorStatus, StatusVariant> = {
	up: 'default',
	down: 'destructive',
	unknown: 'outline',
	paused: 'secondary',
};

export function statusLabel(status: MonitorStatus): string {
	return LABELS[status];
}

export function statusVariant(status: MonitorStatus): StatusVariant {
	return VARIANTS[status];
}
