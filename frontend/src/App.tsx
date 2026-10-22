import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownLeft, ArrowLeftRight, ArrowUpRight, Banknote, ChevronDown, ChevronLeft, ChevronRight, Eye, EyeOff, Filter, Landmark, LockKeyhole, LogOut, Menu, RefreshCw, UsersRound, Wallet, X } from "lucide-react";

type Profile = {
  id: string;
  username: string;
  role: "admin" | "user";
};

const dashboardCards = [
  { id: "income", label: "Income summary" },
  { id: "balance", label: "Total cash account balance" },
  { id: "spending", label: "Spending this month" },
  { id: "net", label: "Monthly net" },
  { id: "expendable_balance", label: "Current expendable balance" },
  { id: "credit_balance", label: "Current total credit balance" },
  { id: "statement_balance", label: "Total statement balance" },
  { id: "projected_income", label: "Projected expendable income" },
];
const defaultDashboardCardOrder = dashboardCards.map((card) => card.id);

function normalizeDashboardCardOrder(order: string[] | undefined) {
  const known = new Set(defaultDashboardCardOrder);
  const normalized = (order ?? []).filter((card, index, cards) => known.has(card) && cards.indexOf(card) === index);
  return [...normalized, ...defaultDashboardCardOrder.filter((card) => !normalized.includes(card))];
}

type DashboardPreferences = {
  hidden_cards: string[];
  card_order: string[];
  excluded_spend_categories: string[];
  excluded_liquidity_transactions: string[];
};

type DashboardMetrics = {
  current_expendable_balance: number | null;
  current_total_credit_balance: number;
  statement_balance_due: number | null;
  projected_expendable_income: number | null;
};

type ManualCreditStatement = {
  account_id: string;
  account_name: string;
  due_date: string;
  statement_date: string | null;
  statement_balance: number;
};

type FinanceSettings = {
  expendable_cash_account_id: string | null;
  manual_statements: ManualCreditStatement[];
  plaid_statements: ManualCreditStatement[];
};

type ProjectionSettings = {
  biweekly_pay_amount: number | null;
  biweekly_payday_anchor: string | null;
  wife_pay_rates: Record<string, number>;
  federal_holidays: string[];
  days_off: string[];
};

type WifePayProjection = {
  pay_date: string;
  work_period_start: string;
  work_period_end: string;
  workdays: number;
  amount: number | null;
};

type ScheduledExpenseFrequency = "one_time" | "weekly" | "monthly" | "bimonthly" | "annual";

type ScheduledExpense = {
  expense_id: number;
  name: string;
  amount: number;
  frequency: ScheduledExpenseFrequency;
  first_due_date: string;
};

type ScheduledExpenseDraft = {
  key: string;
  name: string;
  amount: string;
  frequency: ScheduledExpenseFrequency;
  first_due_date: string;
};

type ScheduledExpenseOccurrence = {
  expense_id: number;
  name: string;
  due_date: string;
  amount: number;
};

type ProjectionDeposit = {
  pay_date: string;
  amount: number;
};

type IncomeProjectionPeriod = {
  spend_month_start: string;
  spend_month_end: string;
  your_pay_total: number | null;
  your_pay_periods: ProjectionDeposit[];
  wife_pay_total: number | null;
  projected_expendable_income: number | null;
  wife_pay_periods: WifePayProjection[];
  missing_workday_counts: number[];
  scheduled_expense_total: number;
  scheduled_expenses: ScheduledExpenseOccurrence[];
};

type ProjectionData = {
  settings: ProjectionSettings;
  federal_holiday_options: { id: string; label: string }[];
  expenses: ScheduledExpense[];
  periods: IncomeProjectionPeriod[];
};

type Account = {
  account_id: string;
  name: string;
  official_name: string | null;
  mask: string | null;
  type: string;
  subtype: string;
  current_balance: number | null;
  iso_currency_code: string | null;
  demo_data: boolean;
  statement_close_day: number | null;
  statement_due_day: number | null;
};

const accountCategories = [
  { type: "depository", label: "Cash accounts" },
  { type: "credit", label: "Credit accounts" },
  { type: "loan", label: "Loan accounts" },
];

type Transaction = {
  transaction_id: string;
  account_id: string;
  name: string;
  merchant_name: string | null;
  amount: number;
  date: string;
  pending: boolean;
  category_primary: string | null;
  category_detailed: string | null;
  iso_currency_code: string | null;
  hidden_from_users?: boolean;
  category_id: string | null;
  has_category_match: boolean;
};

type TransactionTabID = "depository" | "credit" | "loan" | "other";

type TransactionCategory = {
  category_id: string;
  label: string;
  category_type: "income" | "expense";
  excluded_from_totals: boolean;
};

type LiquidityCategory = {
  label: string;
  amount: number;
};

type LiquiditySpendMonth = {
  spend_month: string;
  categories: LiquidityCategory[];
  projected_expendable_income: number | null;
  scheduled_expense_total: number;
};

type DashboardLiquidity = {
  months: LiquiditySpendMonth[];
  unconfigured_credit_accounts: string[];
};

const liquidityChartColors = ["#198568", "#4a9b78", "#79b58e", "#a6c89e", "#d0d99c", "#e5b86d", "#d98268", "#9e829b", "#6f8ba5"];

function SpendingPieChart({
  month,
  showForecast,
  canGoPrevious,
  canGoNext,
  onPrevious,
  onNext,
}: {
  month: LiquiditySpendMonth;
  showForecast: boolean;
  canGoPrevious: boolean;
  canGoNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
}) {
  const total = month.categories.reduce((sum, category) => sum + category.amount, 0);
  const projectedLiquidity = month.projected_expendable_income === null
    ? null
    : month.projected_expendable_income - month.scheduled_expense_total - total;
  const [year, monthNumber] = month.spend_month.split("-").map(Number);
  const periodLabel = getStatisticsRange("spend", month.spend_month).label;
  let startAngle = -Math.PI / 2;
  const slices = month.categories.map((category, index) => {
    const angle = total > 0 ? (category.amount / total) * Math.PI * 2 : 0;
    const endAngle = startAngle + angle;
    const startX = 100 + 82 * Math.cos(startAngle);
    const startY = 100 + 82 * Math.sin(startAngle);
    const endX = 100 + 82 * Math.cos(endAngle);
    const endY = 100 + 82 * Math.sin(endAngle);
    const path = month.categories.length === 1
      ? ""
      : `M 100 100 L ${startX} ${startY} A 82 82 0 ${angle > Math.PI ? 1 : 0} 1 ${endX} ${endY} Z`;
    startAngle = endAngle;
    return { ...category, color: liquidityChartColors[index % liquidityChartColors.length], path };
  });

  return (
    <article className="liquidity-month">
      <div className="liquidity-month-heading">
        <div>
          <h3>{new Date(year, monthNumber - 1, 1).toLocaleDateString(undefined, { month: "long", year: "numeric" })}</h3>
          <span>{periodLabel}</span>
        </div>
        <div className="liquidity-chart-navigation" aria-label="Spend month charts">
          <button type="button" aria-label="Previous spend month chart" onClick={onPrevious} disabled={!canGoPrevious}>
            <ChevronLeft size={18} />
          </button>
          <button type="button" aria-label="Following spend month chart" onClick={onNext} disabled={!canGoNext}>
            <ChevronRight size={18} />
          </button>
        </div>
      </div>
      {total > 0 ? (
        <div className="liquidity-chart-content">
          <svg className="liquidity-pie" viewBox="0 0 200 200" role="img" aria-label={`Spending by category totaling ${money(total)}`}>
            {slices.map((slice) => slice.path
              ? <path key={slice.label} d={slice.path} fill={slice.color}><title>{slice.label}: {money(slice.amount)}</title></path>
              : <circle key={slice.label} cx="100" cy="100" r="82" fill={slice.color}><title>{slice.label}: {money(slice.amount)}</title></circle>)}
          </svg>
          <ul className="liquidity-legend">
            {slices.map((slice) => (
              <li key={slice.label}>
                <span className="liquidity-legend-color" style={{ backgroundColor: slice.color }} />
                <span className="liquidity-legend-label">{slice.label}</span>
                <strong>{money(slice.amount)}</strong>
              </li>
            ))}
          </ul>
        </div>
      ) : <div className="liquidity-empty">No expenses assigned to this spend month yet.</div>}
      {showForecast && (
        <dl className="liquidity-forecast">
          <div><dt>Projected income</dt><dd>{month.projected_expendable_income === null ? "Incomplete" : money(month.projected_expendable_income)}</dd></div>
          <div><dt>Scheduled expenses</dt><dd>−{money(month.scheduled_expense_total)}</dd></div>
          <div><dt>Expenses incurred</dt><dd>−{money(total)}</dd></div>
          <div className="liquidity-forecast-remaining">
            <dt>Projected liquidity remaining</dt>
            <dd className={projectedLiquidity === null ? "" : projectedLiquidity >= 0 ? "positive-text" : "negative-text"}>
              {projectedLiquidity === null ? "Incomplete" : money(projectedLiquidity)}
            </dd>
          </div>
        </dl>
      )}
    </article>
  );
}

declare global {
  interface Window {
    Plaid?: {
      create: (options: {
        token: string;
        onSuccess: (publicToken: string) => void;
        onExit: (error: unknown) => void;
      }) => { open: () => void };
    };
  }
}

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...options?.headers },
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

function money(value: number | null, currency: string | null = "USD") {
  if (value === null) return "—";
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: currency || "USD",
  }).format(value);
}

function transactionDateKey(date: Date) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function monthKey(date: Date) {
  return transactionDateKey(date).slice(0, 7);
}

function getCurrentSpendMonth(today = new Date()) {
  const month = today.getDate() >= 4
    ? new Date(today.getFullYear(), today.getMonth(), 1)
    : new Date(today.getFullYear(), today.getMonth() - 1, 1);
  return monthKey(month);
}

function getInitialMonthHistoryStart(today = new Date()) {
  return monthKey(new Date(today.getFullYear(), today.getMonth() - 12, 1));
}

function getStatisticsRange(period: "spend" | "calendar", selectedMonth: string) {
  const [year, month] = selectedMonth.split("-").map(Number);
  const start = new Date(year, month - 1, period === "spend" ? 4 : 1);
  const end = period === "calendar"
    ? new Date(year, month, 0)
    : new Date(year, month, 3);
  const formatDate = (date: Date) => date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  return {
    start: transactionDateKey(start),
    end: transactionDateKey(end),
    label: `${formatDate(start)} – ${formatDate(end)}`,
  };
}

function displayDate(date: string, options: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", year: "numeric" }) {
  const [year, month, day] = date.slice(0, 10).split("-").map(Number);
  return new Date(year, month - 1, day).toLocaleDateString(undefined, options);
}

export default function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [activePage, setActivePage] = useState<"overview" | "administration" | "transactions" | "projections">(() => {
    if (window.location.hash === "#administration") return "administration";
    if (window.location.hash === "#transactions") return "transactions";
    if (window.location.hash === "#projections") return "projections";
    return "overview";
  });
  const [adminSection, setAdminSection] = useState<"users" | "finance">("users");
  const [incomeDisplay, setIncomeDisplay] = useState<"expendable" | "total">("expendable");
  const [statisticsPeriod, setStatisticsPeriod] = useState<"spend" | "calendar">("spend");
  const [selectedOverviewMonth, setSelectedOverviewMonth] = useState(() => getCurrentSpendMonth());
  const [monthHistoryStart, setMonthHistoryStart] = useState(() => getInitialMonthHistoryStart());
  const [monthPickerOpen, setMonthPickerOpen] = useState(false);
  const [transactionTab, setTransactionTab] = useState<TransactionTabID>("depository");
  const [selectedTransactionAccountIDs, setSelectedTransactionAccountIDs] = useState<string[] | null>(null);
  const [transactionPage, setTransactionPage] = useState(1);
  const [transactionPageSize, setTransactionPageSize] = useState(25);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [dashboardPreferencesOpen, setDashboardPreferencesOpen] = useState(false);
  const [projectionAssumptionsOpen, setProjectionAssumptionsOpen] = useState(false);
  const [scheduledExpensesOpen, setScheduledExpensesOpen] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [hiddenCards, setHiddenCards] = useState<string[]>([]);
  const [excludedSpendCategories, setExcludedSpendCategories] = useState<string[]>([]);
  const [excludedLiquidityTransactions, setExcludedLiquidityTransactions] = useState<string[]>([]);
  const [cardOrder, setCardOrder] = useState(defaultDashboardCardOrder);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [replacementPassword, setReplacementPassword] = useState("");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [transactionCategories, setTransactionCategories] = useState<TransactionCategory[]>([]);
  const [pendingCategoryChange, setPendingCategoryChange] = useState<{ item: Transaction; categoryID: string } | null>(null);
  const [dashboardLiquidity, setDashboardLiquidity] = useState<DashboardLiquidity | null>(null);
  const [liquidityLoading, setLiquidityLoading] = useState(false);
  const [selectedLiquidityMonthIndex, setSelectedLiquidityMonthIndex] = useState(1);
  const [dashboardMetrics, setDashboardMetrics] = useState<DashboardMetrics>({
    current_expendable_balance: null,
    current_total_credit_balance: 0,
    statement_balance_due: null,
    projected_expendable_income: null,
  });
  const [metricsRefreshKey, setMetricsRefreshKey] = useState(0);
  const [financeSettings, setFinanceSettings] = useState<FinanceSettings | null>(null);
  const [financeSettingsLoading, setFinanceSettingsLoading] = useState(false);
  const [expendableAccountSelection, setExpendableAccountSelection] = useState("");
  const [statementAccountSelection, setStatementAccountSelection] = useState("");
  const [statementDueDate, setStatementDueDate] = useState(() => transactionDateKey(new Date()));
  const [statementBalanceInput, setStatementBalanceInput] = useState("");
  const [projectionData, setProjectionData] = useState<ProjectionData | null>(null);
  const [projectionRefreshKey, setProjectionRefreshKey] = useState(0);
  const [projectionLoading, setProjectionLoading] = useState(false);
  const [projectionPayAmount, setProjectionPayAmount] = useState("");
  const [projectionPaydayAnchor, setProjectionPaydayAnchor] = useState("");
  const [wifePayRates, setWifePayRates] = useState<Record<string, string>>({});
  const [selectedFederalHolidays, setSelectedFederalHolidays] = useState<string[]>([]);
  const [projectionDaysOff, setProjectionDaysOff] = useState<string[]>([]);
  const [newProjectionDayOff, setNewProjectionDayOff] = useState("");
  const [scheduledExpenseDrafts, setScheduledExpenseDrafts] = useState<ScheduledExpenseDraft[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const monthPickerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const syncPageFromHash = () => {
      if (window.location.hash === "#administration") setActivePage("administration");
      else if (window.location.hash === "#transactions") setActivePage("transactions");
      else if (window.location.hash === "#projections") setActivePage("projections");
      else setActivePage("overview");
    };
    window.addEventListener("hashchange", syncPageFromHash);
    return () => window.removeEventListener("hashchange", syncPageFromHash);
  }, []);

  useEffect(() => {
    if (activePage !== "overview") setDashboardPreferencesOpen(false);
    if (activePage !== "projections") setProjectionAssumptionsOpen(false);
    if (activePage !== "projections") setScheduledExpensesOpen(false);
  }, [activePage]);

  useEffect(() => {
    const storageKey = "snappy-finance-overview-month-history-start";
    const validMonth = /^\d{4}-(0[1-9]|1[0-2])$/;
    try {
      const storedMonth = window.localStorage.getItem(storageKey);
      if (storedMonth && validMonth.test(storedMonth)) {
        if (storedMonth < monthHistoryStart) setMonthHistoryStart(storedMonth);
        else if (storedMonth > monthHistoryStart) window.localStorage.setItem(storageKey, monthHistoryStart);
      } else {
        window.localStorage.setItem(storageKey, monthHistoryStart);
      }
    } catch (err) {
      console.error("Could not persist Overview month picker history.", err);
      setError("Could not save the Overview month picker history on this device.");
    }
  }, [monthHistoryStart]);

  useEffect(() => {
    if (!monthPickerOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMonthPickerOpen(false);
    };
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (event.target instanceof Node && !monthPickerRef.current?.contains(event.target)) {
        setMonthPickerOpen(false);
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    document.addEventListener("pointerdown", closeOnOutsideClick);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("pointerdown", closeOnOutsideClick);
    };
  }, [monthPickerOpen]);

  useEffect(() => {
    if (!sidebarOpen && !dashboardPreferencesOpen && !projectionAssumptionsOpen && !scheduledExpensesOpen && !pendingCategoryChange) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setSidebarOpen(false);
        setDashboardPreferencesOpen(false);
        setProjectionAssumptionsOpen(false);
        setScheduledExpensesOpen(false);
        setPendingCategoryChange(null);
      }
    };
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [dashboardPreferencesOpen, pendingCategoryChange, projectionAssumptionsOpen, scheduledExpensesOpen, sidebarOpen]);

  function navigateToPage(page: "overview" | "administration" | "transactions" | "projections") {
    setActivePage(page);
    setSidebarOpen(false);
    setDashboardPreferencesOpen(false);
    setProjectionAssumptionsOpen(false);
    setScheduledExpensesOpen(false);
    setMonthPickerOpen(false);
    if (window.location.hash !== `#${page}`) window.location.hash = page;
  }

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [accountRows, transactionRows, categoryRows] = await Promise.all([
        api<Account[]>("/api/accounts"),
        api<Transaction[]>("/api/transactions"),
        api<TransactionCategory[]>("/api/transaction-categories"),
      ]);
      setAccounts(accountRows);
      setTransactions(transactionRows);
      setTransactionCategories(categoryRows);
      setMetricsRefreshKey((current) => current + 1);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load financial data");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    api<{ authenticated: boolean; user: Profile | null }>("/api/auth/session")
      .then(async ({ authenticated: value, user }) => {
        setAuthenticated(value);
        setProfile(user);
        if (user) {
          const preferences = await api<DashboardPreferences>("/api/me/preferences");
          setHiddenCards(preferences.hidden_cards);
          setCardOrder(normalizeDashboardCardOrder(preferences.card_order));
          setExcludedSpendCategories(preferences.excluded_spend_categories ?? []);
          setExcludedLiquidityTransactions(preferences.excluded_liquidity_transactions ?? []);
          if (user.role === "admin") {
            setProfiles(await api<Profile[]>("/api/admin/users"));
          }
          await refresh();
        }
      })
      .catch(() => {
        setAuthenticated(false);
        setProfile(null);
      });
  }, [refresh]);

  const statisticsRange = useMemo(
    () => getStatisticsRange(statisticsPeriod, selectedOverviewMonth),
    [selectedOverviewMonth, statisticsPeriod],
  );
  useEffect(() => {
    if (!profile) return;
    let cancelled = false;
    const params = new URLSearchParams({
      start: statisticsRange.start,
      end: statisticsRange.end,
      spend_month: selectedOverviewMonth,
    });
    api<DashboardMetrics>(`/api/dashboard/metrics?${params.toString()}`)
      .then((metrics) => {
        if (!cancelled) setDashboardMetrics(metrics);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load dashboard finance metrics");
      });
    return () => {
      cancelled = true;
    };
  }, [metricsRefreshKey, profile, selectedOverviewMonth, statisticsRange]);

  useEffect(() => {
    if (!profile) return;
    let cancelled = false;
    setLiquidityLoading(true);
    const params = new URLSearchParams({ spend_month: getCurrentSpendMonth() });
    api<DashboardLiquidity>(`/api/dashboard/liquidity?${params.toString()}`)
      .then((liquidity) => {
        if (!cancelled) setDashboardLiquidity(liquidity);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load spend-month liquidity");
      })
      .finally(() => {
        if (!cancelled) setLiquidityLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [excludedLiquidityTransactions, excludedSpendCategories, metricsRefreshKey, profile]);

  useEffect(() => {
    if (profile?.role !== "admin" || activePage !== "administration" || adminSection !== "finance") return;
    let cancelled = false;
    setFinanceSettingsLoading(true);
    api<FinanceSettings>("/api/admin/finance-settings")
      .then((settings) => {
        if (cancelled) return;
        setFinanceSettings(settings);
        setExpendableAccountSelection(settings.expendable_cash_account_id ?? "");
        if (!statementAccountSelection) {
          setStatementAccountSelection(accounts.find((account) => account.type.toLowerCase() === "credit")?.account_id ?? "");
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load financial management settings");
      })
      .finally(() => {
        if (!cancelled) setFinanceSettingsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activePage, adminSection, accounts, profile, metricsRefreshKey, statementAccountSelection]);

  useEffect(() => {
    if (!profile || activePage !== "projections") return;
    let cancelled = false;
    setProjectionLoading(true);
    api<ProjectionData>("/api/projections")
      .then((data) => {
        if (cancelled) return;
        setProjectionData(data);
        setProjectionPayAmount(data.settings.biweekly_pay_amount?.toString() ?? "");
        setProjectionPaydayAnchor(data.settings.biweekly_payday_anchor?.slice(0, 10) ?? "");
        setWifePayRates(Object.fromEntries(
          Object.entries(data.settings.wife_pay_rates).map(([days, amount]) => [days, amount.toString()]),
        ));
        setSelectedFederalHolidays(data.settings.federal_holidays);
        setProjectionDaysOff(data.settings.days_off.map((date) => date.slice(0, 10)));
        setScheduledExpenseDrafts(data.expenses.map((expense) => ({
          key: String(expense.expense_id),
          name: expense.name,
          amount: expense.amount.toString(),
          frequency: expense.frequency,
          first_due_date: expense.first_due_date.slice(0, 10),
        })));
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load income projections");
      })
      .finally(() => {
        if (!cancelled) setProjectionLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activePage, profile?.id, projectionRefreshKey]);

  const latestOverviewMonth = monthKey(new Date());
  const overviewMonthOptions = useMemo(() => {
    const startMonth = monthHistoryStart > latestOverviewMonth ? latestOverviewMonth : monthHistoryStart;
    const [startYear, startMonthNumber] = startMonth.split("-").map(Number);
    const [endYear, endMonthNumber] = latestOverviewMonth.split("-").map(Number);
    const months: string[] = [];
    let year = startYear;
    let month = startMonthNumber;
    while (year < endYear || (year === endYear && month <= endMonthNumber)) {
      months.push(`${year}-${String(month).padStart(2, "0")}`);
      if (month === 12) {
        year += 1;
        month = 1;
      } else {
        month += 1;
      }
    }
    return months.reverse();
  }, [latestOverviewMonth, monthHistoryStart]);
  const displayedMonth = new Date(
    Number(selectedOverviewMonth.slice(0, 4)),
    Number(selectedOverviewMonth.slice(5, 7)) - 1,
    1,
  );
  const displayedMonthName = displayedMonth.toLocaleDateString(undefined, { month: "long" });

  const totals = useMemo(() => {
    const categoriesByID = new Map(transactionCategories.map((category) => [category.category_id, category]));
    const current = transactions.filter(
      (item) => {
        if (item.date < statisticsRange.start || item.date > statisticsRange.end) return false;
        const category = item.category_id ? categoriesByID.get(item.category_id) : undefined;
        const transactionType = item.amount < 0 ? "income" : "expense";
        return !(category?.category_type === transactionType && category.excluded_from_totals);
      },
    );
    const spent = current.filter((item) => item.amount > 0).reduce((sum, item) => sum + item.amount, 0);
    const income = current.filter((item) => item.amount < 0).reduce((sum, item) => sum + Math.abs(item.amount), 0);
    const expendableIncome = current
      .filter((item) => item.amount < 0 && item.category_id === "expendable_income")
      .reduce((sum, item) => sum + Math.abs(item.amount), 0);
    return { spent, income, expendableIncome, net: income - spent };
  }, [transactions, transactionCategories, statisticsRange]);

  const accountGroups = useMemo(() => {
    const groups = accountCategories.map(({ type, label }) => ({
      label,
      accounts: accounts.filter((account) => account.type.toLowerCase() === type),
    }));
    const otherAccounts = accounts.filter(
      (account) => !accountCategories.some(({ type }) => account.type.toLowerCase() === type),
    );
    if (otherAccounts.length > 0) groups.push({ label: "Other accounts", accounts: otherAccounts });
    return groups;
  }, [accounts]);
  const cashAccounts = useMemo(
    () => accounts.filter((account) => account.type.toLowerCase() === "depository"),
    [accounts],
  );
  const totalCashBalance = cashAccounts.reduce((sum, account) => sum + (account.current_balance ?? 0), 0);

  const accountsByID = useMemo(
    () => new Map(accounts.map((account) => [account.account_id, account])),
    [accounts],
  );
  const transactionTabs = useMemo(() => {
    const accountTypes = new Map(accounts.map((account) => [account.account_id, account.type.toLowerCase()]));
    const knownTypes = new Set(["depository", "credit", "loan"]);
    const hasOtherTransactions = transactions.some((item) => !knownTypes.has(accountTypes.get(item.account_id) ?? ""));
    const hasOtherAccounts = accounts.some((account) => !knownTypes.has(account.type.toLowerCase()));
    return [
      { id: "depository" as const, label: "Cash accounts" },
      { id: "credit" as const, label: "Credit accounts" },
      { id: "loan" as const, label: "Loan accounts" },
      ...(hasOtherTransactions || hasOtherAccounts ? [{ id: "other" as const, label: "Other accounts" }] : []),
    ];
  }, [accounts, transactions]);
  const activeTransactionTab = transactionTabs.some((tab) => tab.id === transactionTab)
    ? transactionTab
    : "depository";
  const transactionAccounts = useMemo(
    () => accounts.filter((account) => activeTransactionTab === "other"
      ? !["depository", "credit", "loan"].includes(account.type.toLowerCase())
      : account.type.toLowerCase() === activeTransactionTab),
    [accounts, activeTransactionTab],
  );
  const visibleTransactions = useMemo(() => {
    return transactions.filter((item) => {
      const type = accountsByID.get(item.account_id)?.type.toLowerCase() ?? "";
      const matchesType = activeTransactionTab === "other"
        ? !["depository", "credit", "loan"].includes(type)
        : type === activeTransactionTab;
      return matchesType && (selectedTransactionAccountIDs === null || selectedTransactionAccountIDs.includes(item.account_id));
    });
  }, [accountsByID, activeTransactionTab, selectedTransactionAccountIDs, transactions]);
  const transactionPageCount = Math.max(1, Math.ceil(visibleTransactions.length / transactionPageSize));
  const currentTransactionPage = Math.min(transactionPage, transactionPageCount);
  const pageTransactions = useMemo(() => {
    const start = (currentTransactionPage - 1) * transactionPageSize;
    return visibleTransactions.slice(start, start + transactionPageSize);
  }, [currentTransactionPage, transactionPageSize, visibleTransactions]);
  const firstTransactionNumber = visibleTransactions.length === 0
    ? 0
    : (currentTransactionPage - 1) * transactionPageSize + 1;
  const lastTransactionNumber = Math.min(currentTransactionPage * transactionPageSize, visibleTransactions.length);

  const pageTitle = activePage === "administration"
    ? "Administration"
    : activePage === "transactions" ? "Transactions"
      : activePage === "projections" ? "Projections" : "Overview";
  const pageEyebrow = activePage === "administration"
    ? "ADMINISTRATION"
    : activePage === "projections" ? "INCOME FORECAST" : "YOUR FINANCES";
  const currentSpendMonth = getCurrentSpendMonth();
  const currentPeriodLabel = statisticsPeriod === "spend"
    ? selectedOverviewMonth === currentSpendMonth ? "THIS CYCLE" : "SPEND CYCLE"
    : selectedOverviewMonth === latestOverviewMonth ? "THIS MONTH" : "SELECTED MONTH";

  function updateTransactionAccountSelection(accountID: string, checked: boolean) {
    setSelectedTransactionAccountIDs((current) => {
      const selected = current ?? transactionAccounts.map((account) => account.account_id);
      return checked
        ? [...new Set([...selected, accountID])]
        : selected.filter((id) => id !== accountID);
    });
    setTransactionPage(1);
  }

  async function signIn(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const result = await api<{ user: Profile }>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      setProfile(result.user);
      setAuthenticated(true);
      setUsername("");
      setPassword("");
      const preferences = await api<DashboardPreferences>("/api/me/preferences");
      setHiddenCards(preferences.hidden_cards);
      setCardOrder(normalizeDashboardCardOrder(preferences.card_order));
      setExcludedSpendCategories(preferences.excluded_spend_categories ?? []);
      setExcludedLiquidityTransactions(preferences.excluded_liquidity_transactions ?? []);
      if (result.user.role === "admin") {
        setProfiles(await api<Profile[]>("/api/admin/users"));
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign in failed");
    }
  }

  async function savePreferences(cardID: string, visible: boolean) {
    const nextHidden = visible
      ? hiddenCards.filter((card) => card !== cardID)
      : [...hiddenCards, cardID];
    try {
      const result = await api<DashboardPreferences>("/api/me/preferences", {
        method: "PUT",
        body: JSON.stringify({
          hidden_cards: nextHidden,
          card_order: cardOrder,
          excluded_spend_categories: excludedSpendCategories,
          excluded_liquidity_transactions: excludedLiquidityTransactions,
        }),
      });
      setHiddenCards(result.hidden_cards);
      setCardOrder(normalizeDashboardCardOrder(result.card_order));
      setExcludedSpendCategories(result.excluded_spend_categories ?? []);
      setExcludedLiquidityTransactions(result.excluded_liquidity_transactions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save dashboard preferences");
    }
  }

  async function saveLiquidityCategory(categoryID: string, excluded: boolean) {
    const nextExcluded = excluded
      ? [...excludedSpendCategories, categoryID]
      : excludedSpendCategories.filter((id) => id !== categoryID);
    try {
      const result = await api<DashboardPreferences>("/api/me/preferences", {
        method: "PUT",
        body: JSON.stringify({
          hidden_cards: hiddenCards,
          card_order: cardOrder,
          excluded_spend_categories: nextExcluded,
          excluded_liquidity_transactions: excludedLiquidityTransactions,
        }),
      });
      setExcludedSpendCategories(result.excluded_spend_categories ?? []);
      setExcludedLiquidityTransactions(result.excluded_liquidity_transactions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save liquidity category preferences");
    }
  }

  async function saveLiquidityTransaction(transactionID: string, excluded: boolean) {
    const nextExcluded = excluded
      ? [...excludedLiquidityTransactions, transactionID]
      : excludedLiquidityTransactions.filter((id) => id !== transactionID);
    try {
      const result = await api<DashboardPreferences>("/api/me/preferences", {
        method: "PUT",
        body: JSON.stringify({
          hidden_cards: hiddenCards,
          card_order: cardOrder,
          excluded_spend_categories: excludedSpendCategories,
          excluded_liquidity_transactions: nextExcluded,
        }),
      });
      setExcludedLiquidityTransactions(result.excluded_liquidity_transactions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save transaction liquidity preference");
    }
  }

  async function moveDashboardCard(cardID: string, direction: -1 | 1) {
    const currentIndex = cardOrder.indexOf(cardID);
    const nextIndex = currentIndex + direction;
    if (currentIndex < 0 || nextIndex < 0 || nextIndex >= cardOrder.length) return;
    const nextOrder = [...cardOrder];
    [nextOrder[currentIndex], nextOrder[nextIndex]] = [nextOrder[nextIndex], nextOrder[currentIndex]];
    try {
      const result = await api<DashboardPreferences>("/api/me/preferences", {
        method: "PUT",
        body: JSON.stringify({
          hidden_cards: hiddenCards,
          card_order: nextOrder,
          excluded_spend_categories: excludedSpendCategories,
          excluded_liquidity_transactions: excludedLiquidityTransactions,
        }),
      });
      setHiddenCards(result.hidden_cards);
      setCardOrder(normalizeDashboardCardOrder(result.card_order));
      setExcludedSpendCategories(result.excluded_spend_categories ?? []);
      setExcludedLiquidityTransactions(result.excluded_liquidity_transactions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save dashboard card order");
    }
  }

  async function setTransactionVisibility(item: Transaction) {
    const hiddenFromUsers = !item.hidden_from_users;
    setError("");
    setNotice("");
    try {
      await api("/api/admin/transactions/visibility", {
        method: "PATCH",
        body: JSON.stringify({
          transaction_id: item.transaction_id,
          hidden_from_users: hiddenFromUsers,
        }),
      });
      setTransactions((current) => current.map((transaction) =>
        transaction.transaction_id === item.transaction_id
          ? { ...transaction, hidden_from_users: hiddenFromUsers }
          : transaction,
      ));
      setNotice(hiddenFromUsers
        ? "Transaction hidden from household users."
        : "Transaction visible to household users.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update transaction visibility");
    }
  }

  function requestTransactionCategoryChange(item: Transaction, categoryID: string) {
    if (categoryID === (item.category_id ?? "")) return;
    if (categoryID && item.has_category_match) {
      setPendingCategoryChange({ item, categoryID });
      return;
    }
    void setTransactionCategory(item, categoryID, "single");
  }

  function applyPendingCategoryChange(scope: "single" | "related") {
    if (!pendingCategoryChange) return;
    const { item, categoryID } = pendingCategoryChange;
    setPendingCategoryChange(null);
    void setTransactionCategory(item, categoryID, scope);
  }

  async function setTransactionCategory(item: Transaction, categoryID: string, scope: "single" | "related") {
    setError("");
    setNotice("");
    try {
      const result = await api<{
        category_id: string | null;
        matched_transaction_ids: string[];
        matching_rule_applied: boolean;
      }>("/api/admin/transactions/category", {
        method: "PATCH",
        body: JSON.stringify({ transaction_id: item.transaction_id, category_id: categoryID, scope }),
      });
      const matchedIDs = new Set(result.matched_transaction_ids);
      setTransactions((current) => current.map((transaction) =>
        matchedIDs.has(transaction.transaction_id)
          ? { ...transaction, category_id: result.category_id }
          : transaction,
      ));
      const label = transactionCategories.find((category) => category.category_id === categoryID)?.label;
      if (!categoryID) {
        setNotice("Using the bank category for this transaction. Similar transactions remain categorized automatically.");
      } else if (scope === "single") {
        setNotice(item.has_category_match
          ? `Applied ${label ?? "Other"} to this transaction only. Related transactions and future matches are unchanged.`
          : `Applied ${label ?? "Other"} to this transaction only because it has no stable description for matching related transactions.`);
      } else if (result.matching_rule_applied) {
        const count = result.matched_transaction_ids.length;
        const matchScope = item.amount < 0 ? " in this account" : "";
        setNotice(`Applied ${label ?? "Other"} to ${count} matching transaction${count === 1 ? "" : "s"}. Future matches${matchScope} will use this category.`);
      } else {
        setNotice(`Categorized this transaction as ${label ?? "Other"}, but no stable description was available to categorize similar transactions.`);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not categorize transaction");
    }
  }

  async function setCategoryExcluded(category: TransactionCategory, excluded: boolean) {
    setError("");
    setNotice("");
    try {
      await api(`/api/admin/transaction-categories/${encodeURIComponent(category.category_id)}`, {
        method: "PATCH",
        body: JSON.stringify({ excluded_from_totals: excluded }),
      });
      setTransactionCategories((current) => current.map((item) =>
        item.category_id === category.category_id
          ? { ...item, excluded_from_totals: excluded }
          : item,
      ));
      const totalName = category.category_type === "income" ? "income" : "spending";
      setNotice(excluded
        ? `${category.label} transactions excluded from ${totalName} for all profiles.`
        : `${category.label} transactions included in ${totalName} for all profiles.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update category settings");
    }
  }

  async function saveExpendableAccount(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    try {
      await api("/api/admin/finance-settings/expendable-account", {
        method: "PUT",
        body: JSON.stringify({ account_id: expendableAccountSelection || null }),
      });
      setNotice("Current expendable balance account saved.");
      setMetricsRefreshKey((current) => current + 1);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save the expendable cash account");
    }
  }

  async function saveCreditCardCycle(event: React.FormEvent<HTMLFormElement>, accountID: string) {
    event.preventDefault();
    setError("");
    setNotice("");
    const form = new FormData(event.currentTarget);
    const statementCloseDay = Number(form.get("statement_close_day"));
    const statementDueDay = Number(form.get("statement_due_day"));
    if (!Number.isInteger(statementCloseDay) || statementCloseDay < 1 || statementCloseDay > 31 ||
      !Number.isInteger(statementDueDay) || statementDueDay < 1 || statementDueDay > 31) {
      setError("Statement close and due days must be between 1 and 31.");
      return;
    }
    try {
      await api("/api/admin/finance-settings/credit-card-cycle", {
        method: "PUT",
        body: JSON.stringify({
          account_id: accountID,
          statement_close_day: statementCloseDay,
          statement_due_day: statementDueDay,
        }),
      });
      setNotice("Credit card statement timing saved.");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save credit card statement timing");
    }
  }

  async function saveProjectionSettings(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    const payAmount = Number(projectionPayAmount);
    const [anchorYear, anchorMonth, anchorDay] = projectionPaydayAnchor.split("-").map(Number);
    const anchorDate = new Date(anchorYear, anchorMonth - 1, anchorDay);
    if (!projectionPaydayAnchor || !Number.isFinite(payAmount) || payAmount < 0 || anchorDate.getDay() !== 4) {
      setError("Enter your net pay amount and a Thursday payday anchor.");
      return;
    }
    const parsedRates: Record<string, number> = {};
    for (const [days, value] of Object.entries(wifePayRates)) {
      if (!value.trim()) continue;
      const amount = Number(value);
      if (!Number.isFinite(amount) || amount < 0) {
        setError(`Enter a valid non-negative net pay amount for ${days} workdays.`);
        return;
      }
      parsedRates[days] = amount;
    }
    for (let days = 8; days <= 12; days += 1) {
      if (parsedRates[String(days)] === undefined) {
        setError(`Enter your wife's net pay amount for ${days} workdays.`);
        return;
      }
    }
    try {
      await api("/api/admin/projections/settings", {
        method: "PUT",
        body: JSON.stringify({
          biweekly_pay_amount: payAmount,
          biweekly_payday_anchor: projectionPaydayAnchor,
          wife_pay_rates: parsedRates,
          federal_holidays: selectedFederalHolidays,
          days_off: projectionDaysOff,
        }),
      });
      setNotice("Income assumptions saved.");
      setProjectionRefreshKey((current) => current + 1);
      setMetricsRefreshKey((current) => current + 1);
      setProjectionAssumptionsOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save projection assumptions");
      setProjectionAssumptionsOpen(false);
    }
  }

  function addProjectionDayOff() {
    if (!newProjectionDayOff || projectionDaysOff.includes(newProjectionDayOff)) return;
    setProjectionDaysOff((current) => [...current, newProjectionDayOff].sort());
    setNewProjectionDayOff("");
  }

  function addScheduledExpenseDraft() {
    setScheduledExpenseDrafts((current) => [
      ...current,
      {
        key: crypto.randomUUID(),
        name: "",
        amount: "",
        frequency: "monthly",
        first_due_date: transactionDateKey(new Date()),
      },
    ]);
  }

  async function saveScheduledExpenses(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    const expenses: Omit<ScheduledExpense, "expense_id">[] = [];
    for (const expense of scheduledExpenseDrafts) {
      const name = expense.name.trim();
      const amount = Number(expense.amount);
      if (!name || name.length > 100 || !expense.first_due_date || !Number.isFinite(amount) || amount < 0) {
        setError("Enter a category, valid non-negative amount, and first due date for every scheduled expense.");
        return;
      }
      expenses.push({
        name,
        amount,
        frequency: expense.frequency,
        first_due_date: expense.first_due_date,
      });
    }
    try {
      await api("/api/admin/projections/expenses", {
        method: "PUT",
        body: JSON.stringify({ expenses }),
      });
      setNotice("Scheduled expenses saved.");
      setProjectionRefreshKey((current) => current + 1);
      setScheduledExpensesOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save scheduled expenses");
    }
  }

  async function saveManualStatement(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    const amount = Number(statementBalanceInput);
    if (!Number.isFinite(amount) || amount < 0) {
      setError("Statement balance must be zero or greater.");
      return;
    }
    try {
      await api("/api/admin/finance-settings/credit-card-statement", {
        method: "PUT",
        body: JSON.stringify({
          account_id: statementAccountSelection,
          due_date: statementDueDate,
          statement_balance: amount,
        }),
      });
      setStatementBalanceInput("");
      setNotice("Manual credit card statement saved.");
      setMetricsRefreshKey((current) => current + 1);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save manual statement");
    }
  }

  async function deleteManualStatement(statement: ManualCreditStatement) {
    setError("");
    setNotice("");
    try {
      await api("/api/admin/finance-settings/credit-card-statement", {
        method: "DELETE",
        body: JSON.stringify({ account_id: statement.account_id, due_date: statement.due_date.slice(0, 10) }),
      });
      setNotice("Manual credit card statement removed.");
      setMetricsRefreshKey((current) => current + 1);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not remove manual statement");
    }
  }

  const transactionsPanel = (
    <section className="panel" id="transactions">
      <div className="panel-heading"><div><h2>Recent transactions</h2><p>{profile?.role === "admin" ? "Categories apply to similar past and future transactions; manage exclusions in Administration." : "Your latest activity across accounts"}</p></div></div>
      <div className="transactions-layout">
        <div className="transaction-tabs" role="group" aria-label="Filter transactions by account type">
          {transactionTabs.map((tab) => (
            <button
              type="button"
              key={tab.id}
              className={`transaction-tab ${activeTransactionTab === tab.id ? "active" : ""}`}
              aria-pressed={activeTransactionTab === tab.id}
              onClick={() => {
                setTransactionTab(tab.id);
                setSelectedTransactionAccountIDs(null);
                setTransactionPage(1);
              }}
            >{tab.label}</button>
          ))}
        </div>
        <div className="transactions-content">
          {transactionAccounts.length > 1 && (
            <div className="transaction-account-filters" role="group" aria-label="Filter transactions by account">
              <strong>Accounts</strong>
              <label className="transaction-account-filter">
                <input
                  type="checkbox"
                  checked={selectedTransactionAccountIDs === null || transactionAccounts.every((account) => selectedTransactionAccountIDs.includes(account.account_id))}
                  onChange={(event) => {
                    setSelectedTransactionAccountIDs(event.target.checked ? null : []);
                    setTransactionPage(1);
                  }}
                />
                All accounts
              </label>
              {transactionAccounts.map((account) => (
                <label className="transaction-account-filter" key={account.account_id}>
                  <input
                    type="checkbox"
                    checked={selectedTransactionAccountIDs === null || selectedTransactionAccountIDs.includes(account.account_id)}
                    onChange={(event) => updateTransactionAccountSelection(account.account_id, event.target.checked)}
                  />
                  {account.official_name || account.name}{account.mask ? ` ···· ${account.mask}` : ""}
                </label>
              ))}
            </div>
          )}
          <div className="transaction-list-toolbar">
            <span>Showing {firstTransactionNumber}–{lastTransactionNumber} of {visibleTransactions.length}</span>
            <label className="page-size-control">
              Items per page
              <select
                value={transactionPageSize}
                onChange={(event) => {
                  setTransactionPageSize(Number(event.target.value));
                  setTransactionPage(1);
                }}
                aria-label="Transactions per page"
              >
                {[25, 50, 75, 100].map((pageSize) => (
                  <option key={pageSize} value={pageSize}>{pageSize}</option>
                ))}
              </select>
            </label>
          </div>
          {loading ? <div className="empty-state">Loading transactions…</div> : visibleTransactions.length === 0 ? (
            <div className="empty-state compact">
              {transactions.length === 0
                ? "Transactions will appear here after your first account sync."
                : selectedTransactionAccountIDs !== null && selectedTransactionAccountIDs.length === 0
                  ? "Select one or more accounts to see transactions."
                  : `No transactions for ${transactionTabs.find((tab) => tab.id === activeTransactionTab)?.label.toLowerCase() ?? "this account type"} yet.`}
            </div>
          ) : (
            <div className="transaction-list">
              {pageTransactions.map((item) => (
                <div className={`transaction-row ${item.hidden_from_users ? "hidden-transaction" : ""}`} key={item.transaction_id}>
                  <span className={`transaction-icon ${item.amount < 0 ? "income-icon" : ""}`}>{item.amount < 0 ? <ArrowDownLeft size={17} /> : <ArrowUpRight size={17} />}</span>
                  <div className="transaction-info"><strong>{item.merchant_name || item.name}</strong><span>{transactionCategories.find((category) => category.category_id === item.category_id)?.label || item.category_detailed || item.category_primary || "Uncategorized"} · {new Date(`${item.date}T12:00:00`).toLocaleDateString()} · {accountsByID.get(item.account_id)?.official_name || accountsByID.get(item.account_id)?.name || "Unknown account"}</span></div>
                  <strong className={item.amount < 0 ? "positive-text" : ""}>{item.amount < 0 ? "+" : "−"}{money(Math.abs(item.amount), item.iso_currency_code)}{item.pending && <small className="pending"> Pending</small>}</strong>
                  {(profile?.role === "admin" || (item.amount > 0 && ["depository", "credit"].includes(accountsByID.get(item.account_id)?.type.toLowerCase() ?? ""))) && (
                    <div className="transaction-actions">
                      {item.amount > 0 && ["depository", "credit"].includes(accountsByID.get(item.account_id)?.type.toLowerCase() ?? "") && (
                        <label className="transaction-liquidity-toggle" title="Only affects your liquidity chart and forecast">
                          <input
                            type="checkbox"
                            checked={excludedLiquidityTransactions.includes(item.transaction_id)}
                            onChange={(event) => void saveLiquidityTransaction(item.transaction_id, event.target.checked)}
                          />
                          Exclude from liquidity
                        </label>
                      )}
                      {profile?.role === "admin" && <>
                        <select
                          className="category-select"
                          value={item.category_id ?? ""}
                          aria-label={`Category for ${item.merchant_name || item.name}`}
                          onChange={(event) => requestTransactionCategoryChange(item, event.target.value)}
                        >
                          <option value="">Bank category</option>
                          {transactionCategories.filter((category) => category.category_type === (item.amount < 0 ? "income" : "expense")).map((category) => (
                            <option key={category.category_id} value={category.category_id}>{category.label}</option>
                          ))}
                        </select>
                        <button
                          type="button"
                          className="visibility-button"
                          onClick={() => void setTransactionVisibility(item)}
                          aria-label={item.hidden_from_users ? "Show transaction to household users" : "Hide transaction from household users"}
                          title={item.hidden_from_users ? "Show to household users" : "Hide from household users"}
                        >
                          {item.hidden_from_users ? <Eye size={15} /> : <EyeOff size={15} />}
                          <span>{item.hidden_from_users ? "Show" : "Hide"}</span>
                        </button>
                      </>}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
          <nav className="transaction-pagination" aria-label="Transaction pages">
            <button
              type="button"
              className="secondary-button"
              disabled={currentTransactionPage <= 1}
              onClick={() => setTransactionPage(currentTransactionPage - 1)}
            >Previous</button>
            <span>Page {currentTransactionPage} of {transactionPageCount}</span>
            <button
              type="button"
              className="secondary-button"
              disabled={currentTransactionPage >= transactionPageCount}
              onClick={() => setTransactionPage(currentTransactionPage + 1)}
            >Next</button>
          </nav>
        </div>
      </div>
    </section>
  );

  async function createProfile(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    try {
      await api("/api/admin/users", {
        method: "POST",
        body: JSON.stringify({ username: newUsername, password: newPassword }),
      });
      setNewUsername("");
      setNewPassword("");
      setProfiles(await api<Profile[]>("/api/admin/users"));
      setNotice("Household profile created.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create profile");
    }
  }

  async function changePassword(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    try {
      await api("/api/me/password", {
        method: "POST",
        body: JSON.stringify({ current_password: currentPassword, new_password: replacementPassword }),
      });
      setCurrentPassword("");
      setReplacementPassword("");
      setNotice("Password changed.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not change password");
    }
  }

  async function connectAccount() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      if (!window.Plaid) throw new Error("Plaid Link did not load. Check your network and reload.");
      const { link_token: linkToken } = await api<{ link_token: string }>("/api/plaid/link-token", { method: "POST" });
      const handler = window.Plaid.create({
        token: linkToken,
        onSuccess: async (publicToken) => {
          try {
            await api("/api/plaid/exchange", {
              method: "POST",
              body: JSON.stringify({ public_token: publicToken }),
            });
            setNotice("Your account is connected. Transactions are syncing.");
            await refresh();
          } catch (err) {
            setError(err instanceof Error ? err.message : "Could not finish connecting your account");
          } finally {
            setBusy(false);
          }
        },
        onExit: (err) => {
          if (err) setError("Plaid Link closed before the connection was completed.");
          setBusy(false);
        },
      });
      handler.open();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not start Plaid Link");
      setBusy(false);
    }
  }

  async function syncNow() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api("/api/plaid/sync", { method: "POST" });
      setNotice("Sync complete.");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sync failed");
    } finally {
      setBusy(false);
    }
  }

  async function signOut() {
    try {
      await api("/api/auth/logout", { method: "POST" });
      setAuthenticated(false);
      setProfile(null);
      setIncomeDisplay("expendable");
      navigateToPage("overview");
      setAdminSection("users");
      setHiddenCards([]);
      setCardOrder(defaultDashboardCardOrder);
      setProfiles([]);
      setAccounts([]);
      setTransactions([]);
      setTransactionCategories([]);
      setFinanceSettings(null);
      setProjectionData(null);
      setProjectionPayAmount("");
      setProjectionPaydayAnchor("");
      setWifePayRates({});
      setSelectedFederalHolidays([]);
      setProjectionDaysOff([]);
      setScheduledExpenseDrafts([]);
      setDashboardMetrics({
        current_expendable_balance: null,
        current_total_credit_balance: 0,
        statement_balance_due: null,
        projected_expendable_income: null,
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign out");
    }
  }

  const projectionsPanel = (
    <>
      {profile?.role === "admin" && (
        <>
          <button
            type="button"
            className={`dashboard-preferences-backdrop ${projectionAssumptionsOpen ? "visible" : ""}`}
            aria-label="Close income assumptions"
            aria-hidden={!projectionAssumptionsOpen}
            tabIndex={projectionAssumptionsOpen ? 0 : -1}
            onClick={() => setProjectionAssumptionsOpen(false)}
          />
          <aside
            id="projection-assumptions"
            className={`dashboard-preferences-drawer projection-assumptions-drawer ${projectionAssumptionsOpen ? "open" : ""}`}
            role="dialog"
            aria-modal="true"
            aria-labelledby="projection-assumptions-title"
            aria-hidden={!projectionAssumptionsOpen}
          >
            <section className="panel settings-panel projection-assumptions-drawer-panel">
              <div className="panel-heading preferences-drawer-heading">
                <div><h2 id="projection-assumptions-title">Income assumptions</h2><p>Enter take-home pay and schedule details to calculate income for the current spend cycle and the next 12 cycles.</p></div>
                <button
                  type="button"
                  className="preferences-drawer-close"
                  aria-label="Close income assumptions"
                  onClick={() => setProjectionAssumptionsOpen(false)}
                ><X size={18} /></button>
              </div>
              {projectionLoading && !projectionData ? (
                <div className="empty-state compact">Loading projection settings…</div>
              ) : (
                <form className="projection-settings-form" onSubmit={saveProjectionSettings}>
              <div className="projection-pay-inputs">
                <label>Your fixed net biweekly pay
                  <input type="number" min="0" step="0.01" value={projectionPayAmount} onChange={(event) => setProjectionPayAmount(event.target.value)} required />
                </label>
                <label>A known Thursday payday to anchor the 14-day schedule
                  <input type="date" value={projectionPaydayAnchor} onChange={(event) => setProjectionPaydayAnchor(event.target.value)} required />
                </label>
              </div>
              <p className="projection-help">Your deposit is projected every 14 days from this date, including federal holidays.</p>

              <fieldset className="projection-holidays">
                <legend>Federal holidays your wife does not work</legend>
                <div className="projection-holiday-grid">
                  {(projectionData?.federal_holiday_options ?? []).map((holiday) => (
                    <label className="preference-option" key={holiday.id}>
                      <input
                        type="checkbox"
                        checked={selectedFederalHolidays.includes(holiday.id)}
                        onChange={(event) => setSelectedFederalHolidays((current) => (
                          event.target.checked
                            ? [...current, holiday.id]
                            : current.filter((item) => item !== holiday.id)
                        ))}
                      />
                      {holiday.label}
                    </label>
                  ))}
                </div>
              </fieldset>
              <p className="projection-help">Holiday selections recur annually; observed weekday dates are excluded when a holiday falls on a weekend. Planned time off is entered as specific dates.</p>

              <div className="projection-rates">
                <div className="projection-section-heading">
                  <h3>Jan’s net pay by workdays in a 15-day period</h3>
                  <span>One shared amount table is used for both half-month periods.</span>
                </div>
                <div className="projection-rate-grid">
                  {Array.from({ length: 13 }, (_, workdays) => (
                    <label key={workdays}>
                      {workdays} workdays{workdays >= 8 && workdays <= 12 ? " *" : ""}
                      <input
                        type="number"
                        min="0"
                        step="0.01"
                        value={wifePayRates[String(workdays)] ?? ""}
                        onChange={(event) => setWifePayRates((current) => ({ ...current, [workdays]: event.target.value }))}
                        required={workdays >= 8 && workdays <= 12}
                      />
                    </label>
                  ))}
                </div>
                <p className="projection-help">Amounts for 8–12 workdays are required. If planned time off reduces a period below 8 workdays, add that count’s net pay amount here for a complete projection.</p>
              </div>

              <div className="projection-days-off">
                <div className="projection-section-heading">
                  <h3>Planned days off</h3>
                  <span>Specific dates are applied to any projected work period they fall in.</span>
                </div>
                <div className="projection-day-off-entry">
                  <label>Add a date off<input type="date" value={newProjectionDayOff} onChange={(event) => setNewProjectionDayOff(event.target.value)} /></label>
                  <button className="secondary-button" type="button" onClick={addProjectionDayOff} disabled={!newProjectionDayOff || projectionDaysOff.includes(newProjectionDayOff)}>Add day off</button>
                </div>
                {projectionDaysOff.length > 0 ? (
                  <ul className="projection-day-off-list">
                    {projectionDaysOff.map((date) => (
                      <li key={date}>
                        <span>{displayDate(date)}</span>
                        <button className="secondary-button" type="button" onClick={() => setProjectionDaysOff((current) => current.filter((item) => item !== date))}>Remove</button>
                      </li>
                    ))}
                  </ul>
                ) : <p className="projection-help">No planned days off entered.</p>}
              </div>
              <div className="projection-save-row">
                <button className="primary-button" type="submit">Save income assumptions</button>
              </div>
                </form>
              )}
            </section>
          </aside>
          <button
            type="button"
            className={`dashboard-preferences-backdrop ${scheduledExpensesOpen ? "visible" : ""}`}
            aria-label="Close scheduled expenses"
            aria-hidden={!scheduledExpensesOpen}
            tabIndex={scheduledExpensesOpen ? 0 : -1}
            onClick={() => setScheduledExpensesOpen(false)}
          />
          <aside
            id="scheduled-expenses"
            className={`dashboard-preferences-drawer scheduled-expenses-drawer ${scheduledExpensesOpen ? "open" : ""}`}
            role="dialog"
            aria-modal="true"
            aria-labelledby="scheduled-expenses-title"
            aria-hidden={!scheduledExpensesOpen}
          >
            <section className="panel settings-panel scheduled-expenses-drawer-panel">
              <div className="panel-heading preferences-drawer-heading">
                <div><h2 id="scheduled-expenses-title">Scheduled expenses</h2><p>Create recurring expense categories and assign each an amount, frequency, and first due date.</p></div>
                <button
                  type="button"
                  className="preferences-drawer-close"
                  aria-label="Close scheduled expenses"
                  onClick={() => setScheduledExpensesOpen(false)}
                ><X size={18} /></button>
              </div>
              {projectionLoading && !projectionData ? (
                <div className="empty-state compact">Loading scheduled expenses…</div>
              ) : (
                <form className="projection-settings-form" onSubmit={saveScheduledExpenses}>
                  <p className="projection-help">Each schedule starts on its first due date. One-time expenses occur once; weekly repeats every 7 days, and monthly, every-two-months, and annual schedules follow that date’s calendar day.</p>
                  {scheduledExpenseDrafts.length > 0 ? (
                    <div className="scheduled-expense-list">
                      {scheduledExpenseDrafts.map((expense) => (
                        <div className="scheduled-expense-editor" key={expense.key}>
                          <label>Expense category
                            <input
                              type="text"
                              maxLength={100}
                              value={expense.name}
                              onChange={(event) => setScheduledExpenseDrafts((current) => current.map((item) => item.key === expense.key ? { ...item, name: event.target.value } : item))}
                              required
                            />
                          </label>
                          <label>Amount
                            <input
                              type="number"
                              min="0"
                              step="0.01"
                              value={expense.amount}
                              onChange={(event) => setScheduledExpenseDrafts((current) => current.map((item) => item.key === expense.key ? { ...item, amount: event.target.value } : item))}
                              required
                            />
                          </label>
                          <label>Frequency
                            <select
                              value={expense.frequency}
                              onChange={(event) => setScheduledExpenseDrafts((current) => current.map((item) => item.key === expense.key ? { ...item, frequency: event.target.value as ScheduledExpenseFrequency } : item))}
                            >
                              <option value="one_time">One time</option>
                              <option value="weekly">Weekly</option>
                              <option value="monthly">Monthly</option>
                              <option value="bimonthly">Bimonthly (every 2 months)</option>
                              <option value="annual">Annual</option>
                            </select>
                          </label>
                          <label>First due date
                            <input
                              type="date"
                              value={expense.first_due_date}
                              onChange={(event) => setScheduledExpenseDrafts((current) => current.map((item) => item.key === expense.key ? { ...item, first_due_date: event.target.value } : item))}
                              required
                            />
                          </label>
                          <button
                            type="button"
                            className="secondary-button scheduled-expense-remove"
                            aria-label={`Remove ${expense.name || "scheduled expense"}`}
                            onClick={() => setScheduledExpenseDrafts((current) => current.filter((item) => item.key !== expense.key))}
                          >Remove</button>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <p className="projection-help">No scheduled expenses. Add one to include recurring bills in the forecast.</p>
                  )}
                  <div className="projection-save-row scheduled-expense-actions">
                    <button
                      type="button"
                      className="secondary-button"
                      onClick={addScheduledExpenseDraft}
                      disabled={scheduledExpenseDrafts.length >= 200}
                    >Add expense</button>
                    <button className="primary-button" type="submit">Save scheduled expenses</button>
                  </div>
                </form>
              )}
            </section>
          </aside>
        </>
      )}

      <section className="panel projections-panel">
        <div className="panel-heading">
          <div><h2>Projected expendable income</h2><p>Automatic take-home pay forecast by spend cycle (4th–3rd), including the current cycle and the next 12.</p></div>
          {profile?.role === "admin" && (
            <div className="projection-drawer-actions">
              <button
                type="button"
                className="secondary-button projection-assumptions-button"
                aria-expanded={projectionAssumptionsOpen}
                aria-controls="projection-assumptions"
                onClick={() => {
                  setScheduledExpensesOpen(false);
                  setProjectionAssumptionsOpen(true);
                }}
              ><Filter size={16} /> Income assumptions</button>
              <button
                type="button"
                className="secondary-button projection-assumptions-button"
                aria-expanded={scheduledExpensesOpen}
                aria-controls="scheduled-expenses"
                onClick={() => {
                  setProjectionAssumptionsOpen(false);
                  setScheduledExpensesOpen(true);
                }}
              ><Filter size={16} /> Scheduled expenses</button>
            </div>
          )}
        </div>
        {projectionLoading && !projectionData ? (
          <div className="empty-state">Calculating projections…</div>
        ) : !projectionData ? (
          <div className="empty-state">Income projections could not be loaded.</div>
        ) : (
          <div className="projection-period-list">
            {projectionData.periods.map((period) => (
              <article className="projection-period" key={period.spend_month_start}>
                <div className="projection-period-heading">
                  <div><h3>{displayDate(period.spend_month_start)} – {displayDate(period.spend_month_end)}</h3><span>Spend cycle</span></div>
                  <strong className={period.projected_expendable_income === null ? "projection-incomplete" : ""}>
                    {period.projected_expendable_income === null ? "Incomplete" : money(period.projected_expendable_income)}
                  </strong>
                </div>
                {period.scheduled_expenses.length > 0 && (
                  <div className="projection-scheduled-total">
                    <span>Scheduled expenses</span>
                    <strong>−{money(period.scheduled_expense_total)}</strong>
                  </div>
                )}
                <div className="projection-liquidity">
                  <span>Liquidity</span>
                  <strong>
                    {period.projected_expendable_income === null
                      ? "Incomplete"
                      : money(period.projected_expendable_income - period.scheduled_expense_total)}
                  </strong>
                </div>
                {period.your_pay_total === null && <p className="projection-warning">Enter Saby’s net pay and a payday anchor to calculate this portion.</p>}
                {period.missing_workday_counts.length > 0 && (
                  <p className="projection-warning">Missing Jan’s pay rate{period.missing_workday_counts.length === 1 ? "" : "s"} for {period.missing_workday_counts.join(", ")} workday{period.missing_workday_counts.length === 1 ? "" : "s"}.</p>
                )}
                <details className="projection-pay-details">
                  <summary>Details</summary>
                  <div className="projection-details-grid">
                    <section className="projection-detail-column">
                      <h4>Income deposits</h4>
                      <div className="projection-income-totals">
                        <div><span>Saby’s pay</span><strong>{money(period.your_pay_total)}</strong></div>
                        <div><span>Jan’s pay</span><strong>{money(period.wife_pay_total)}</strong></div>
                      </div>
                      {period.your_pay_periods.map((payPeriod) => (
                        <div className="projection-pay-period" key={`your-${payPeriod.pay_date}`}>
                          <span>Saby’s pay · {displayDate(payPeriod.pay_date)}</span>
                          <strong>{money(payPeriod.amount)}</strong>
                        </div>
                      ))}
                      {period.wife_pay_periods.map((payPeriod) => (
                        <div className="projection-pay-period" key={`wife-${payPeriod.pay_date}-${payPeriod.work_period_start}`}>
                          <span>Jan’s pay · {displayDate(payPeriod.pay_date)} · worked {displayDate(payPeriod.work_period_start)}–{displayDate(payPeriod.work_period_end)} ({payPeriod.workdays} days)</span>
                          <strong>{money(payPeriod.amount)}</strong>
                        </div>
                      ))}
                      {period.your_pay_periods.length === 0 && period.wife_pay_periods.length === 0 && (
                        <p className="projection-detail-empty">No income deposits are scheduled for this cycle.</p>
                      )}
                    </section>
                    <section className="projection-detail-column">
                      <h4>Scheduled expenses</h4>
                      {period.scheduled_expenses.map((expense) => (
                        <div className="projection-pay-period projection-expense-detail" key={`${expense.expense_id}-${expense.due_date}`}>
                          <span>{expense.name} · due {displayDate(expense.due_date)}</span>
                          <strong>{money(expense.amount)}</strong>
                        </div>
                      ))}
                      {period.scheduled_expenses.length === 0 ? (
                        <p className="projection-detail-empty">No scheduled expenses are due this cycle.</p>
                      ) : (
                        <div className="projection-expense-detail-total">
                          <span>Total scheduled expenses</span>
                          <strong>−{money(period.scheduled_expense_total)}</strong>
                        </div>
                      )}
                    </section>
                  </div>
                </details>
              </article>
            ))}
          </div>
        )}
      </section>
    </>
  );

  const passwordPanel = (
    <details className="panel password-panel">
      <summary>Change my password</summary>
      <form className="profile-form" onSubmit={changePassword}>
        <label>Current password<input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></label>
        <label>New password<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={replacementPassword} onChange={(event) => setReplacementPassword(event.target.value)} required /></label>
        <button className="secondary-button" type="submit">Update password</button>
      </form>
    </details>
  );

  if (authenticated === null) {
    return <main className="loading-screen">Loading your workspace…</main>;
  }

  if (!authenticated) {
    return (
      <main className="login-screen">
        <form className="login-card" onSubmit={signIn}>
          <div className="brand-mark"><Wallet size={21} /></div>
          <p className="eyebrow">SNAPPY FINANCE</p>
          <h1>Your money, in focus.</h1>
          <p className="muted">Sign in to your private finance dashboard.</p>
          <label htmlFor="username">Username</label>
          <input
            id="username"
            autoComplete="username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            required
          />
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
          {error && <p className="error-message">{error}</p>}
          <button className="primary-button full-width" type="submit">Sign in</button>
          <p className="secure-note"><LockKeyhole size={14} /> Your data stays on your server.</p>
        </form>
      </main>
    );
  }

  return (
    <main className="app-shell">
      <aside className={`sidebar ${sidebarOpen ? "sidebar-open" : ""}`} id="app-sidebar">
        <div className="brand"><span className="brand-mark"><Wallet size={19} /></span><span>snappy<span className="brand-light">finance</span></span></div>
        <nav className="sidebar-nav" aria-label="Workspace">
          <div className="nav-label">WORKSPACE</div>
          <button
            className={`nav-item ${activePage === "overview" ? "active" : ""}`}
            onClick={() => navigateToPage("overview")}
            aria-current={activePage === "overview" ? "page" : undefined}
          ><Banknote size={18} /> Overview</button>
          <button
            className={`nav-item ${activePage === "projections" ? "active" : ""}`}
            onClick={() => navigateToPage("projections")}
            aria-current={activePage === "projections" ? "page" : undefined}
          ><ArrowUpRight size={18} /> Projections</button>
          <button
            className={`nav-item ${activePage === "transactions" ? "active" : ""}`}
            onClick={() => navigateToPage("transactions")}
            aria-current={activePage === "transactions" ? "page" : undefined}
          ><ArrowLeftRight size={18} /> Transactions</button>
        </nav>
        <div className="sidebar-bottom">
          <button
            className={`nav-item admin-nav-item ${activePage === "administration" ? "active" : ""}`}
            onClick={() => navigateToPage("administration")}
            aria-current={activePage === "administration" ? "page" : undefined}
          ><UsersRound size={18} /> Administration</button>
          <div className="privacy-card"><LockKeyhole size={16} /><span>Private by design<br /><small>Your data is self-hosted</small></span></div>
          <div className="profile-identity">
            <strong>{profile?.username}</strong>
            <span>{profile?.role === "admin" ? "Administrator" : "Household profile"}</span>
          </div>
          <button className="signout-button" onClick={() => void signOut()}><LogOut size={16} /> Sign out</button>
        </div>
      </aside>
      <div className="mobile-topbar" aria-hidden="true">
        <div className="brand"><span className="brand-mark"><Wallet size={19} /></span><span>snappy<span className="brand-light">finance</span></span></div>
      </div>
      <button
        type="button"
        className="menu-toggle"
        aria-label={sidebarOpen ? "Close navigation menu" : "Open navigation menu"}
        aria-expanded={sidebarOpen}
        aria-controls="app-sidebar"
        onClick={() => setSidebarOpen((open) => !open)}
      >{sidebarOpen ? <X size={20} /> : <Menu size={20} />}</button>
      <button
        type="button"
        className={`sidebar-backdrop ${sidebarOpen ? "visible" : ""}`}
        aria-label="Close navigation menu"
        aria-hidden={!sidebarOpen}
        tabIndex={sidebarOpen ? 0 : -1}
        onClick={() => setSidebarOpen(false)}
      />

      <section className="main-content">
        <header className="topbar">
          <div>
            <p className="eyebrow">{pageEyebrow}</p>
            {activePage === "overview" ? (
              <div className="overview-month-picker" ref={monthPickerRef}>
                <h1 className="overview-month-title">
                  <button
                    type="button"
                    className="overview-month-button"
                    aria-label={`${displayedMonthName} ${displayedMonth.getFullYear()} finances. Choose month.`}
                    aria-expanded={monthPickerOpen}
                    aria-haspopup="dialog"
                    aria-controls="overview-month-picker"
                    onClick={() => setMonthPickerOpen((open) => !open)}
                  >
                    <span>{displayedMonthName}'s finances</span>
                    <span className="overview-month-year">{displayedMonth.getFullYear()}</span>
                    <ChevronDown size={18} />
                  </button>
                </h1>
                {monthPickerOpen && (
                  <div id="overview-month-picker" className="month-picker-popover" role="dialog" aria-label="Choose a calendar month">
                    <label className="month-picker-control">
                      View month
                      <select
                        value={selectedOverviewMonth}
                        onChange={(event) => {
                          setSelectedOverviewMonth(event.target.value);
                          setStatisticsPeriod("calendar");
                          setMonthPickerOpen(false);
                        }}
                      >
                        {overviewMonthOptions.map((monthKey) => {
                          const [year, month] = monthKey.split("-").map(Number);
                          const label = new Date(year, month - 1, 1).toLocaleDateString(undefined, {
                            month: "long",
                            year: "numeric",
                          });
                          return <option key={monthKey} value={monthKey}>{label}</option>;
                        })}
                      </select>
                    </label>
                  </div>
                )}
              </div>
            ) : <h1>{pageTitle}</h1>}
          </div>
          <div className="topbar-actions">
            {activePage === "overview" && (
              <button
                type="button"
                className="secondary-button filter-button"
                aria-expanded={dashboardPreferencesOpen}
                aria-controls="dashboard-preferences"
                onClick={() => setDashboardPreferencesOpen(true)}
              >
                <Filter size={16} /> Filter
              </button>
            )}
            {activePage === "overview" && profile?.role === "admin" && <>
              <button className="secondary-button" onClick={() => void syncNow()} disabled={busy || !accounts.some((account) => !account.demo_data)}>
                <RefreshCw size={16} className={busy ? "spin" : ""} /> Sync
              </button>
              <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>
                <span>+</span> Connect account
              </button>
            </>}
          </div>
        </header>

        {error && <div className="alert error-message">{error}</div>}
        {notice && <div className="alert success-message">{notice}</div>}

        {pendingCategoryChange && (
          <div
            className="category-scope-backdrop"
            role="presentation"
            onMouseDown={(event) => {
              if (event.target === event.currentTarget) setPendingCategoryChange(null);
            }}
          >
            <section className="category-scope-dialog" role="dialog" aria-modal="true" aria-labelledby="category-scope-title">
              <h2 id="category-scope-title">Apply category change</h2>
              <p>
                Apply <strong>{transactionCategories.find((category) => category.category_id === pendingCategoryChange.categoryID)?.label ?? "this category"}</strong> to <strong>{pendingCategoryChange.item.merchant_name || pendingCategoryChange.item.name}</strong>:
              </p>
              <div className="category-scope-actions">
                <button className="secondary-button" type="button" onClick={() => setPendingCategoryChange(null)}>Cancel</button>
                <button className="secondary-button" type="button" onClick={() => applyPendingCategoryChange("single")}>This transaction only</button>
                <button className="primary-button" type="button" onClick={() => applyPendingCategoryChange("related")}>All related transactions</button>
              </div>
              <span className="category-scope-help">“All related” also updates future transactions matched by the automated rule.</span>
            </section>
          </div>
        )}

        {activePage === "administration" ? (
          profile?.role === "admin" ? (
            <>
              <div className="admin-tabs" role="group" aria-label="Administration sections">
                <button
                  type="button"
                  className={`admin-tab ${adminSection === "users" ? "active" : ""}`}
                  aria-pressed={adminSection === "users"}
                  onClick={() => setAdminSection("users")}
                >User management</button>
                <button
                  type="button"
                  className={`admin-tab ${adminSection === "finance" ? "active" : ""}`}
                  aria-pressed={adminSection === "finance"}
                  onClick={() => setAdminSection("finance")}
                >Financial management</button>
              </div>
              {adminSection === "users" ? (
                <>
                  <section className="panel settings-panel">
                    <div className="panel-heading"><div><h2>Household profiles</h2><p>Profiles share financial data; each person has their own dashboard and password.</p></div></div>
                    <div className="profile-list">
                      {profiles.map((item) => (
                        <div className="profile-row" key={item.id}>
                          <strong>{item.username}</strong>
                          <span>{item.role === "admin" ? "Administrator" : "Household user"}</span>
                        </div>
                      ))}
                    </div>
                    <form className="profile-form" onSubmit={createProfile}>
                      <label>New username<input autoComplete="off" minLength={3} maxLength={32} value={newUsername} onChange={(event) => setNewUsername(event.target.value)} required /></label>
                      <label>Temporary password<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={newPassword} onChange={(event) => setNewPassword(event.target.value)} required /></label>
                      <button className="primary-button" type="submit">Create household profile</button>
                    </form>
                  </section>
                  {passwordPanel}
                </>
              ) : (
                <>
                  <section className="panel settings-panel">
                    <div className="panel-heading">
                      <div><h2>Overview balance settings</h2><p>Choose the cash account shown as the current expendable balance on every profile’s Overview.</p></div>
                    </div>
                    {financeSettingsLoading || !financeSettings ? (
                      <div className="empty-state compact">{financeSettingsLoading ? "Loading finance settings…" : "Finance settings are unavailable."}</div>
                    ) : (
                      <>
                        <form className="profile-form finance-form" onSubmit={saveExpendableAccount}>
                          <label>Current Expendable balance account
                            <select value={expendableAccountSelection} onChange={(event) => setExpendableAccountSelection(event.target.value)}>
                              <option value="">Select a cash account</option>
                              {cashAccounts.map((account) => (
                                <option key={account.account_id} value={account.account_id}>
                                  {account.official_name || account.name}{account.mask ? ` ···· ${account.mask}` : ""}
                                </option>
                              ))}
                            </select>
                          </label>
                          <button className="secondary-button" type="submit">Save account</button>
                        </form>
                      </>
                    )}
                  </section>

                  <section className="panel settings-panel">
                    <div className="panel-heading">
                      <div><h2>Credit card spend-month timing</h2><p>Set each card’s statement closing day and payment due day so purchases are assigned to the spend month when the payment affects cash liquidity.</p></div>
                    </div>
                    {accounts.filter((account) => account.type.toLowerCase() === "credit").length ? (
                      accounts.filter((account) => account.type.toLowerCase() === "credit").map((account) => (
                        <form className="profile-form finance-form credit-cycle-form" key={account.account_id} onSubmit={(event) => void saveCreditCardCycle(event, account.account_id)}>
                          <div className="credit-cycle-account">
                            <strong>{account.official_name || account.name}</strong>
                            <span>{account.mask ? `···· ${account.mask}` : "Credit account"}{account.statement_close_day && account.statement_due_day ? " · Configured" : " · Not configured"}</span>
                          </div>
                          <label>Statement closes on day
                            <input name="statement_close_day" type="number" min="1" max="31" defaultValue={account.statement_close_day ?? ""} required />
                          </label>
                          <label>Payment due on day
                            <input name="statement_due_day" type="number" min="1" max="31" defaultValue={account.statement_due_day ?? ""} required />
                          </label>
                          <button className="secondary-button" type="submit">Save timing</button>
                        </form>
                      ))
                    ) : <p className="finance-help">Connect a credit account to configure its statement timing.</p>}
                    <p className="finance-help">For example, a card that closes on the 3rd and is due on the 1st will map Sep 4–Oct 3 purchases to the October spend month (Oct 4–Nov 3).</p>
                  </section>

                  <section className="panel settings-panel">
                    <div className="panel-heading">
                      <div><h2>Credit card statement balances</h2><p>Plaid statement data is used where available; enter statements manually when it is not.</p></div>
                    </div>
                    <form className="profile-form finance-form" onSubmit={saveManualStatement}>
                      <label>Credit account
                        <select value={statementAccountSelection} onChange={(event) => setStatementAccountSelection(event.target.value)} required>
                          <option value="">Select a credit account</option>
                          {accounts.filter((account) => account.type.toLowerCase() === "credit").map((account) => (
                            <option key={account.account_id} value={account.account_id}>
                              {account.official_name || account.name}{account.mask ? ` ···· ${account.mask}` : ""}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>Due date<input type="date" value={statementDueDate} onChange={(event) => setStatementDueDate(event.target.value)} required /></label>
                      <label>Statement balance<input type="number" min="0" step="0.01" value={statementBalanceInput} onChange={(event) => setStatementBalanceInput(event.target.value)} required /></label>
                      <button className="primary-button" type="submit" disabled={!accounts.some((account) => account.type.toLowerCase() === "credit")}>Save statement</button>
                    </form>
                    <div className="manual-statement-list">
                      <h3>Plaid-retrieved statements</h3>
                      {financeSettings?.plaid_statements.length ? financeSettings.plaid_statements.map((statement) => (
                        <div className="manual-statement-row" key={`${statement.account_id}-${statement.due_date}`}>
                          <div>
                            <strong>{statement.account_name}</strong>
                            <span>{statement.statement_date ? `Statement ${new Date(`${statement.statement_date.slice(0, 10)}T00:00:00`).toLocaleDateString()} · ` : ""}Due {new Date(`${statement.due_date.slice(0, 10)}T00:00:00`).toLocaleDateString()}</span>
                          </div>
                          <strong>{money(statement.statement_balance)}</strong>
                        </div>
                      )) : <p className="finance-help">No statement balances have been retrieved from Plaid yet.</p>}
                    </div>
                    <div className="manual-statement-list">
                      <h3>Manual entries</h3>
                      {financeSettings?.manual_statements.length ? financeSettings.manual_statements.map((statement) => (
                        <div className="manual-statement-row" key={`${statement.account_id}-${statement.due_date}`}>
                          <div><strong>{statement.account_name}</strong><span>Due {new Date(`${statement.due_date.slice(0, 10)}T00:00:00`).toLocaleDateString()}</span></div>
                          <strong>{money(statement.statement_balance)}</strong>
                          <button className="secondary-button" type="button" onClick={() => void deleteManualStatement(statement)}>Remove</button>
                        </div>
                      )) : <p className="finance-help">No manual statement entries.</p>}
                    </div>
                  </section>

                  <section className="panel settings-panel">
                    <div className="panel-heading">
                      <div><h2>Monthly total exclusions</h2><p>Exclude categories from Spending or Income. Monthly Net uses the adjusted totals for every profile.</p></div>
                    </div>
                    <div className="category-exclusion-groups">
                      {(["expense", "income"] as const).map((categoryType) => (
                        <section className="category-exclusion-group" key={categoryType}>
                          <h3>{categoryType === "expense" ? "Spending" : "Income"}</h3>
                          <div className="preference-list">
                            {transactionCategories.filter((category) => category.category_type === categoryType).map((category) => (
                              <label className="preference-option" key={category.category_id}>
                                <input
                                  type="checkbox"
                                  checked={category.excluded_from_totals}
                                  onChange={(event) => void setCategoryExcluded(category, event.target.checked)}
                                />
                                Exclude {category.label} from {categoryType === "expense" ? "spending" : "income"}
                              </label>
                            ))}
                          </div>
                        </section>
                      ))}
                    </div>
                  </section>
                </>
              )}
            </>
          ) : passwordPanel
        ) : activePage === "transactions" ? (
          <>
            {accounts.some((account) => account.demo_data) && (
              <div className="alert demo-banner">Demo data — these fictional accounts and transactions are for local testing only.</div>
            )}
            {transactionsPanel}
          </>
        ) : activePage === "projections" ? (
          projectionsPanel
        ) : (
          <>
            {accounts.some((account) => account.demo_data) && (
              <div className="alert demo-banner">Demo data — these fictional accounts and transactions are for local testing only.</div>
            )}
            <button
              type="button"
              className={`dashboard-preferences-backdrop ${dashboardPreferencesOpen ? "visible" : ""}`}
              aria-label="Close dashboard filters"
              aria-hidden={!dashboardPreferencesOpen}
              tabIndex={dashboardPreferencesOpen ? 0 : -1}
              onClick={() => setDashboardPreferencesOpen(false)}
            />
            <aside
              id="dashboard-preferences"
              className={`dashboard-preferences-drawer ${dashboardPreferencesOpen ? "open" : ""}`}
              aria-labelledby="dashboard-preferences-title"
              aria-hidden={!dashboardPreferencesOpen}
            >
              <section className="panel settings-panel">
                <div className="panel-heading preferences-drawer-heading">
                  <div><h2 id="dashboard-preferences-title">Dashboard preferences</h2><p>Choose which summary cards and statistics period appear.</p></div>
                  <button
                    type="button"
                    className="preferences-drawer-close"
                    aria-label="Close dashboard filters"
                    onClick={() => setDashboardPreferencesOpen(false)}
                  ><X size={18} /></button>
                </div>
                <div className="preference-list">
                  <strong className="preference-list-heading">Show summary cards</strong>
                  {dashboardCards.map((card) => (
                    <label className="preference-option" key={card.id}>
                      <input
                        type="checkbox"
                        checked={!hiddenCards.includes(card.id)}
                        onChange={(event) => void savePreferences(card.id, event.target.checked)}
                      />
                      {card.label}
                    </label>
                  ))}
                </div>
                <div className="card-order-list">
                  <strong className="preference-list-heading">Card order</strong>
                  {cardOrder.map((cardID, index) => {
                    const card = dashboardCards.find((item) => item.id === cardID);
                    if (!card) return null;
                    return (
                      <div className="card-order-row" key={cardID}>
                        <span>{index + 1}. {card.label}</span>
                        <div>
                          <button type="button" aria-label={`Move ${card.label} up`} disabled={index === 0} onClick={() => void moveDashboardCard(cardID, -1)}>
                            <ChevronDown className="move-card-up" size={15} />
                          </button>
                          <button type="button" aria-label={`Move ${card.label} down`} disabled={index === cardOrder.length - 1} onClick={() => void moveDashboardCard(cardID, 1)}>
                            <ChevronDown size={15} />
                          </button>
                        </div>
                      </div>
                    );
                  })}
                </div>
                <div className="preference-list liquidity-category-preferences">
                  <strong className="preference-list-heading">Include spending categories in liquidity charts</strong>
                  <p>These choices affect your liquidity charts and forecasts only. Categories excluded by the administrator remain excluded for everyone.</p>
                  {transactionCategories.filter((category) => category.category_type === "expense").map((category) => (
                    <label className="preference-option" key={category.category_id}>
                      <input
                        type="checkbox"
                        checked={!excludedSpendCategories.includes(category.category_id)}
                        onChange={(event) => void saveLiquidityCategory(category.category_id, !event.target.checked)}
                      />
                      {category.label}
                    </label>
                  ))}
                </div>
                <div className="statistics-period-control">
                  <div className="statistics-period-copy">
                    <strong>Statistics period</strong>
                    <span>{statisticsPeriod === "spend" ? "Spend calendar (4th–3rd)" : "Calendar month"} · {statisticsRange.label}</span>
                  </div>
                  <div className="statistics-period-options" role="group" aria-label="Statistics period">
                    <button
                      type="button"
                      className={`statistics-period-option ${statisticsPeriod === "spend" ? "active" : ""}`}
                      aria-pressed={statisticsPeriod === "spend"}
                      onClick={() => setStatisticsPeriod("spend")}
                    >Spend calendar</button>
                    <button
                      type="button"
                      className={`statistics-period-option ${statisticsPeriod === "calendar" ? "active" : ""}`}
                      aria-pressed={statisticsPeriod === "calendar"}
                      onClick={() => setStatisticsPeriod("calendar")}
                    >Calendar month</button>
                  </div>
                </div>
              </section>
            </aside>

        <div className="summary-grid">
          {!hiddenCards.includes("income") && <article className="summary-card income-card" style={{ order: cardOrder.indexOf("income") }}>
            <div className="income-summary-heading">
              <div className="summary-label">{incomeDisplay === "expendable" ? "TOTAL EXPENDABLE INCOME" : `TOTAL INCOME ${currentPeriodLabel}`}</div>
              <button
                type="button"
                className="summary-toggle"
                onClick={() => setIncomeDisplay(incomeDisplay === "expendable" ? "total" : "expendable")}
              >View {incomeDisplay === "expendable" ? "total income" : "expendable income"}</button>
            </div>
            <div className="summary-value">{money(incomeDisplay === "expendable" ? totals.expendableIncome : totals.income)}</div>
            <div className="summary-foot positive"><ArrowDownLeft size={15} /> {incomeDisplay === "expendable" ? "Expendable inflows" : "All income inflows"}</div>
          </article>}
          {!hiddenCards.includes("balance") && <article className="summary-card balance-card" style={{ order: cardOrder.indexOf("balance") }}>
            <div className="summary-label">TOTAL CASH ACCOUNT BALANCE</div>
            <div className="summary-value">
              {money(totalCashBalance)}
            </div>
            <div className="summary-foot"><Wallet size={15} /> Across {cashAccounts.length} cash account{cashAccounts.length === 1 ? "" : "s"}</div>
          </article>}
          {!hiddenCards.includes("spending") && <article className="summary-card" style={{ order: cardOrder.indexOf("spending") }}>
            <div className="summary-label">SPENDING {currentPeriodLabel}</div>
            <div className="summary-value">{money(totals.spent)}</div>
            <div className="summary-foot"><ArrowUpRight size={15} /> Outflows</div>
          </article>}
          {!hiddenCards.includes("net") && <article className="summary-card" style={{ order: cardOrder.indexOf("net") }}>
            <div className="summary-label">NET {currentPeriodLabel}</div>
            <div className={`summary-value ${totals.net >= 0 ? "positive-text" : "negative-text"}`}>{money(totals.net)}</div>
            <div className="summary-foot">Income minus spending in selected period</div>
          </article>}
          {!hiddenCards.includes("expendable_balance") && <article className="summary-card" style={{ order: cardOrder.indexOf("expendable_balance") }}>
            <div className="summary-label">CURRENT EXPENDABLE BALANCE</div>
            <div className="summary-value">{money(dashboardMetrics.current_expendable_balance)}</div>
            <div className="summary-foot"><Wallet size={15} /> Admin-selected cash account</div>
          </article>}
          {!hiddenCards.includes("credit_balance") && <article className="summary-card" style={{ order: cardOrder.indexOf("credit_balance") }}>
            <div className="summary-label">CURRENT TOTAL CREDIT BALANCE</div>
            <div className="summary-value">{money(dashboardMetrics.current_total_credit_balance)}</div>
            <div className="summary-foot">Across {accounts.filter((account) => account.type.toLowerCase() === "credit").length} credit account{accounts.filter((account) => account.type.toLowerCase() === "credit").length === 1 ? "" : "s"}</div>
          </article>}
          {!hiddenCards.includes("statement_balance") && <article className="summary-card" style={{ order: cardOrder.indexOf("statement_balance") }}>
            <div className="summary-label">TOTAL STATEMENT BALANCE DUE</div>
            <div className="summary-value">{money(dashboardMetrics.statement_balance_due)}</div>
            <div className="summary-foot">Due {statisticsRange.label}</div>
          </article>}
          {!hiddenCards.includes("projected_income") && <article className="summary-card" style={{ order: cardOrder.indexOf("projected_income") }}>
            <div className="summary-label">PROJECTED EXPENDABLE INCOME</div>
            <div className="summary-value">{money(dashboardMetrics.projected_expendable_income)}</div>
            <div className="summary-foot">Selected spend month</div>
          </article>}
        </div>

        <section className="panel liquidity-panel">
          <div className="panel-heading">
            <div><h2>Expenses affecting liquidity</h2><p>Category totals assigned to the current spend month and the next two spend months.</p></div>
          </div>
          {liquidityLoading && !dashboardLiquidity ? (
            <div className="empty-state">Loading spend-month expenses…</div>
          ) : dashboardLiquidity ? (
            <>
              {dashboardLiquidity.months.length > 0 && (() => {
                const monthIndex = Math.min(selectedLiquidityMonthIndex, dashboardLiquidity.months.length - 1);
                return (
                  <div className="liquidity-month-grid">
                    <SpendingPieChart
                      key={dashboardLiquidity.months[monthIndex].spend_month}
                      month={dashboardLiquidity.months[monthIndex]}
                      showForecast={monthIndex > 0}
                      canGoPrevious={monthIndex > 0}
                      canGoNext={monthIndex < dashboardLiquidity.months.length - 1}
                      onPrevious={() => setSelectedLiquidityMonthIndex((index) => Math.max(0, index - 1))}
                      onNext={() => setSelectedLiquidityMonthIndex((index) => Math.min(dashboardLiquidity.months.length - 1, index + 1))}
                    />
                  </div>
                );
              })()}
              {dashboardLiquidity.unconfigured_credit_accounts.length > 0 && (
                <p className="finance-help liquidity-help">
                  Configure statement timing for {dashboardLiquidity.unconfigured_credit_accounts.join(", ")} in Administration → Financial management to include their credit purchases.
                </p>
              )}
            </>
          ) : <div className="empty-state">Spend-month expenses are unavailable.</div>}
        </section>

        <section className="panel" id="accounts">
          <div className="panel-heading"><div><h2>Accounts</h2><p>Balances from your connected institutions</p></div></div>
          {loading ? <div className="empty-state">Loading accounts…</div> : accounts.length === 0 ? (
            <div className="empty-state">
              <span className="empty-icon"><Landmark size={22} /></span>
              <strong>No accounts connected yet</strong>
              <span>{profile?.role === "admin" ? "Connect a bank to see your balances and transactions." : "Ask your household administrator to connect a bank account."}</span>
              {profile?.role === "admin" && <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>Connect your first account</button>}
            </div>
          ) : (
            <div className="account-list">
              {accountGroups.map((group) => (
                <section className="account-group" key={group.label}>
                  <div className="account-group-heading">
                    <h3>{group.label}</h3>
                    <span>{group.accounts.length}</span>
                  </div>
                  {group.accounts.length === 0 ? (
                    <div className="account-group-empty">No {group.label.toLowerCase()} connected.</div>
                  ) : group.accounts.map((account) => (
                    <div className="account-row" key={account.account_id}>
                      <span className="account-icon"><Landmark size={18} /></span>
                      <div className="account-info"><strong>{account.official_name || account.name}</strong><span>{account.type}{account.subtype ? ` · ${account.subtype}` : ""}{account.mask ? ` ···· ${account.mask}` : ""}{account.demo_data ? " · DEMO" : ""}</span></div>
                      <strong className="account-balance">{money(account.current_balance, account.iso_currency_code)}</strong>
                    </div>
                  ))}
                </section>
              ))}
            </div>
          )}
        </section>

          </>
        )}
        <footer>Snappy Finance · Self-hosted personal finance</footer>
      </section>
    </main>
  );
}
