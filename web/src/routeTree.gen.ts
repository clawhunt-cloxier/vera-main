import { Route as rootRoute } from './routes/__root'
import { Route as PrivacyRoute } from './routes/privacy'
import { Route as OnboardingRoute } from './routes/onboarding'
import { Route as IndexRoute } from './routes/index'
import { Route as SampleSkillRoute } from './routes/sample.$skill'
import { Route as AppIndexRoute } from './routes/app.index'
import { Route as AppWatchlistIndexRoute } from './routes/app.watchlist.index'
import { Route as AppReportsIndexRoute } from './routes/app.reports.index'
import { Route as AppWatchlistIdAlertsRoute } from './routes/app.watchlist.$id.alerts'
import { Route as AppTasksIdRoute } from './routes/app.tasks.$id'
import { Route as AppReportsIdRoute } from './routes/app.reports.$id'
import { Route as AppQuickSkillRoute } from './routes/app.quick.$skill'
import { Route as AppPlanIdRoute } from './routes/app.plan.$id'

const PrivacyRouteWithChildren = PrivacyRoute
const OnboardingRouteWithChildren = OnboardingRoute
const IndexRouteWithChildren = IndexRoute
const SampleSkillRouteWithChildren = SampleSkillRoute
const AppIndexRouteWithChildren = AppIndexRoute
const AppWatchlistIndexRouteWithChildren = AppWatchlistIndexRoute
const AppReportsIndexRouteWithChildren = AppReportsIndexRoute
const AppWatchlistIdAlertsRouteWithChildren = AppWatchlistIdAlertsRoute
const AppTasksIdRouteWithChildren = AppTasksIdRoute
const AppReportsIdRouteWithChildren = AppReportsIdRoute
const AppQuickSkillRouteWithChildren = AppQuickSkillRoute
const AppPlanIdRouteWithChildren = AppPlanIdRoute

export const routeTree = rootRoute.addChildren({
  PrivacyRoute: PrivacyRouteWithChildren,
  OnboardingRoute: OnboardingRouteWithChildren,
  IndexRoute: IndexRouteWithChildren,
  SampleSkillRoute: SampleSkillRouteWithChildren,
  AppIndexRoute: AppIndexRouteWithChildren,
  AppWatchlistIndexRoute: AppWatchlistIndexRouteWithChildren,
  AppReportsIndexRoute: AppReportsIndexRouteWithChildren,
  AppWatchlistIdAlertsRoute: AppWatchlistIdAlertsRouteWithChildren,
  AppTasksIdRoute: AppTasksIdRouteWithChildren,
  AppReportsIdRoute: AppReportsIdRouteWithChildren,
  AppQuickSkillRoute: AppQuickSkillRouteWithChildren,
  AppPlanIdRoute: AppPlanIdRouteWithChildren,
})
