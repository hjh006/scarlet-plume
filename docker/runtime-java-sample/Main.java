import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import io.opentelemetry.api.trace.Span;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.SQLException;
import java.util.Properties;
import java.util.regex.Pattern;

public final class Main {
    private static final String CUSTOMERS_PREFIX = "/runtime/customers/";
    private static final String CUSTOMER_ROUTE = CUSTOMERS_PREFIX + "{id}";
    private static final String UNDOCUMENTED_ROUTE = "/runtime/undocumented";
    private static final Pattern CUSTOMER_ID = Pattern.compile("[a-zA-Z0-9_-]{1,24}");

    public static void main(String[] args) throws IOException {
        String databaseUrl = requiredEnv("SCARLET_RUNTIME_JDBC_URL");
        if (!databaseUrl.startsWith("jdbc:postgresql://")) {
            throw new IllegalArgumentException("The demo requires a PostgreSQL JDBC URL.");
        }
        Properties database = new Properties();
        database.setProperty("user", requiredEnv("SCARLET_RUNTIME_JDBC_USER"));
        database.setProperty("password", requiredEnv("SCARLET_RUNTIME_JDBC_PASSWORD"));
        database.setProperty("connectTimeout", "3");
        database.setProperty("socketTimeout", "3");

        HttpServer server = HttpServer.create(new InetSocketAddress(8080), 16);
        server.createContext("/readyz", exchange -> {
            try (exchange) {
                if (exchange.getRequestURI().getRawPath().equals("/readyz")) {
                    respond(exchange, 200, "ready");
                } else {
                    respond(exchange, 404, "not found");
                }
            }
        });
        server.createContext(CUSTOMERS_PREFIX, exchange -> {
            String path = exchange.getRequestURI().getRawPath();
            boolean matches = path.startsWith(CUSTOMERS_PREFIX)
                && CUSTOMER_ID.matcher(path.substring(CUSTOMERS_PREFIX.length())).matches();
            handle(exchange, CUSTOMER_ROUTE, matches, databaseUrl, database);
        });
        server.createContext(UNDOCUMENTED_ROUTE, exchange ->
            handle(exchange, UNDOCUMENTED_ROUTE,
                exchange.getRequestURI().getRawPath().equals(UNDOCUMENTED_ROUTE), databaseUrl, database));
        server.start();
    }

    private static void handle(HttpExchange exchange, String route, boolean matches, String databaseUrl,
                               Properties database) throws IOException {
        try (exchange) {
            if (!matches) {
                respond(exchange, 404, "not found");
                return;
            }
            // The agent creates the server span. Use the fixed template known to the router.
            Span.current().setAttribute("http.route", route);
            if (!exchange.getRequestMethod().equals("GET")) {
                exchange.getResponseHeaders().set("Allow", "GET");
                respond(exchange, 405, "method not allowed");
                return;
            }
            if (!query(databaseUrl, database)) {
                respond(exchange, 503, "sample database unavailable");
                return;
            }
            respond(exchange, 200, "synthetic PostgreSQL check complete");
        }
    }

    private static boolean query(String databaseUrl, Properties database) {
        try (Connection connection = DriverManager.getConnection(databaseUrl, database)) {
            connection.setReadOnly(true);
            try (var statement = connection.prepareStatement("SELECT 1");
                 var rows = statement.executeQuery()) {
                return rows.next() && rows.getInt(1) == 1;
            }
        } catch (SQLException failure) {
            System.err.println("Runtime sample JDBC check failed.");
            return false;
        }
    }

    private static void respond(HttpExchange exchange, int status, String message) throws IOException {
        byte[] body = (message + "\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().set("Content-Type", "text/plain; charset=utf-8");
        exchange.sendResponseHeaders(status, body.length);
        exchange.getResponseBody().write(body);
    }

    private static String requiredEnv(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            throw new IllegalStateException("Missing environment variable " + name);
        }
        return value;
    }
}
