import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import type { ApiProblem } from '@/lib/api/problems';

export interface ErrorPanelProps {
	title?: string;
	message: string;
	problem?: ApiProblem;
	onRetry?: () => void;
}

export function ErrorPanel({
	title = 'Something went wrong',
	message,
	problem,
	onRetry,
}: ErrorPanelProps) {
	return (
		<Alert variant="destructive">
			<AlertTitle>{title}</AlertTitle>
			<AlertDescription className="space-y-2">
				<p>{message}</p>
				{problem ? (
					<p className="font-mono text-xs text-muted-foreground">
						Code: {problem.code}
					</p>
				) : null}
				{onRetry ? (
					<Button type="button" variant="outline" size="sm" onClick={onRetry}>
						Try again
					</Button>
				) : null}
			</AlertDescription>
		</Alert>
	);
}
