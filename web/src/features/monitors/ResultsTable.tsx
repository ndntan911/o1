import { useEffect, useState } from 'react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Table,
	TableBody,
	TableCaption,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from '@/components/ui/table';
import type { ProbeResult } from '@/lib/api/types';
import { formatLatency, formatRelativeTime } from '@/lib/format';

export interface ResultsTableProps {
	results: ProbeResult[];
	hasNextPage: boolean;
	isFetchingNextPage: boolean;
	onLoadMore: () => void;
}

const DESKTOP_QUERY = '(min-width: 768px)';

function useIsDesktop(): boolean {
	const [isDesktop, setIsDesktop] = useState(false);

	useEffect(() => {
		const media = window.matchMedia?.(DESKTOP_QUERY);
		if (!media) return;

		const update = () => setIsDesktop(media.matches);
		update();
		media.addEventListener?.('change', update);
		return () => media.removeEventListener?.('change', update);
	}, []);

	return isDesktop;
}

function statusLabel(result: ProbeResult): string {
	return result.status === 'up' ? 'Up' : 'Down';
}

function statusVariant(result: ProbeResult): 'default' | 'destructive' {
	return result.status === 'up' ? 'default' : 'destructive';
}

function statusCode(result: ProbeResult): string {
	return result.status_code === null ? '—' : String(result.status_code);
}

function ErrorCell({ result }: { result: ProbeResult }) {
	if (!result.error) return <span>—</span>;

	return (
		<span className="block max-w-64 truncate" title={result.error}>
			{result.error}
		</span>
	);
}

export function ResultsTable({
	results,
	hasNextPage,
	isFetchingNextPage,
	onLoadMore,
}: ResultsTableProps) {
	const isDesktop = useIsDesktop();

	if (results.length === 0) {
		return (
			<p className="text-sm text-muted-foreground">No results in this window</p>
		);
	}

	return (
		<section className="space-y-3" aria-label="Results">
			{isDesktop ? (
				<Table>
					<TableCaption className="sr-only">Probe results</TableCaption>
					<TableHeader>
						<TableRow>
							<TableHead>Checked at</TableHead>
							<TableHead>Status</TableHead>
							<TableHead>Status code</TableHead>
							<TableHead>Latency</TableHead>
							<TableHead>Error</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{results.map((result) => (
							<TableRow key={result.id}>
								<TableCell>
									<span title={result.checked_at}>
										{formatRelativeTime(result.checked_at)}
									</span>
								</TableCell>
								<TableCell>
									<Badge variant={statusVariant(result)}>
										{statusLabel(result)}
									</Badge>
								</TableCell>
								<TableCell>{statusCode(result)}</TableCell>
								<TableCell>{formatLatency(result.latency_ms)}</TableCell>
								<TableCell>
									<ErrorCell result={result} />
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			) : (
				<ul className="space-y-3">
					{results.map((result) => (
						<li
							key={result.id}
							className="space-y-1 rounded-xl border p-3 text-sm"
						>
							<div className="flex items-center justify-between gap-2">
								<span title={result.checked_at}>
									{formatRelativeTime(result.checked_at)}
								</span>
								<Badge variant={statusVariant(result)}>
									{statusLabel(result)}
								</Badge>
							</div>
							<p>
								<span className="text-muted-foreground">Status code </span>
								<span>{statusCode(result)}</span>
							</p>
							<p>
								<span className="text-muted-foreground">Latency </span>
								<span>{formatLatency(result.latency_ms)}</span>
							</p>
							<p>
								<span className="text-muted-foreground">Error </span>
								<ErrorCell result={result} />
							</p>
						</li>
					))}
				</ul>
			)}

			{hasNextPage ? (
				<div className="flex justify-center">
					<Button
						type="button"
						variant="outline"
						disabled={isFetchingNextPage}
						onClick={onLoadMore}
					>
						Load more
					</Button>
				</div>
			) : null}
		</section>
	);
}
