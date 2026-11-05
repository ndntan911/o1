import { Skeleton } from '@/components/ui/skeleton';

export function PageSkeleton() {
	return (
		<div className="space-y-4" role="status" aria-label="Loading">
			<Skeleton className="h-8 w-48" />
			<Skeleton className="h-4 w-80 max-w-full" />
			<div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
				<Skeleton className="h-32" />
				<Skeleton className="h-32" />
				<Skeleton className="h-32" />
			</div>
		</div>
	);
}
