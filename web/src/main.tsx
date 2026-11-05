import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

createRoot(document.getElementById('root')!).render(
	<StrictMode>
		<main className="p-8">o1 dashboard</main>
	</StrictMode>,
);
