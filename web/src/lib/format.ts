const relativeTime = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

export function formatUptime(
	uptime: number | null | undefined,
	checks: number | undefined,
): string {
	if (uptime === null || uptime === undefined || !checks) return 'No data';
	return `${(uptime * 100).toFixed(1)}%`;
}

export function formatLatency(ms: number | null | undefined): string {
	if (ms === null || ms === undefined) return '—';
	return `${Math.round(ms)} ms`;
}

export function formatRelativeTime(
	iso: string,
	now: Date = new Date(),
): string {
	const then = new Date(iso).getTime();
	const deltaSeconds = Math.round((then - now.getTime()) / 1000);
	const absoluteSeconds = Math.abs(deltaSeconds);

	if (absoluteSeconds < 60) {
		return relativeTime.format(deltaSeconds, 'second');
	}
	if (absoluteSeconds < 3600) {
		return relativeTime.format(Math.round(deltaSeconds / 60), 'minute');
	}
	if (absoluteSeconds < 86_400) {
		return relativeTime.format(Math.round(deltaSeconds / 3600), 'hour');
	}
	return relativeTime.format(Math.round(deltaSeconds / 86_400), 'day');
}
