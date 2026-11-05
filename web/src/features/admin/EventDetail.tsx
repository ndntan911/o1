import { useState } from 'react';
import { Button } from '@/components/ui/button';

export interface EventDetailProps {
	detail: unknown;
}

export function EventDetail({ detail }: EventDetailProps) {
	const [open, setOpen] = useState(false);
	const text = JSON.stringify(detail, null, 2) ?? 'null';

	return (
		<div className="space-y-1">
			<Button
				type="button"
				variant="ghost"
				size="xs"
				aria-expanded={open}
				onClick={() => setOpen((current) => !current)}
			>
				Details
			</Button>
			{open ? (
				<pre className="max-h-64 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap">
					{text}
				</pre>
			) : null}
		</div>
	);
}
