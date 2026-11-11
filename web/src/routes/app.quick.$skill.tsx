import { createFileRoute } from '@tanstack/react-router';
import { Quick } from '../pages/app';

export const Route = createFileRoute('/app/quick/$skill')({
  component: () => (
    <Quick skill={Route.useParams().skill as 'stock_analysis' | 'capital_flow' | 'deep_research'} />
  ),
});
