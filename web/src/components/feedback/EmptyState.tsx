import type { ReactNode } from 'react';

export interface EmptyStateProps {
	title: string;
	description?: string;
	action?: ReactNode;
}

export function EmptyState({ title, description, action }: EmptyStateProps) {
	return (
		<section className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-6 py-12 text-center">
			<h2 className="font-heading text-lg font-medium">{title}</h2>
			{description ? (
				<p className="max-w-md text-sm text-muted-foreground">{description}</p>
			) : null}
			{action}
		</section>
	);
}
