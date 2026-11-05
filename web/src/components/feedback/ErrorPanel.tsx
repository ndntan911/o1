import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';

export interface ErrorPanelProps {
	title?: string;
	message: string;
	onRetry?: () => void;
}

export function ErrorPanel({
	title = 'Something went wrong',
	message,
	onRetry,
}: ErrorPanelProps) {
	return (
		<Alert variant="destructive">
			<AlertTitle>{title}</AlertTitle>
			<AlertDescription className="space-y-2">
				<p>{message}</p>
				{onRetry ? (
					<Button type="button" variant="outline" size="sm" onClick={onRetry}>
						Try again
					</Button>
				) : null}
			</AlertDescription>
		</Alert>
	);
}
