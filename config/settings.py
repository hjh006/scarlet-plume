import os
from pathlib import Path

from django.core.exceptions import ImproperlyConfigured


BASE_DIR = Path(__file__).resolve().parent.parent


def require_env(name):
    value = os.environ.get(name, "")
    if not value.strip():
        raise ImproperlyConfigured(f"{name} is required in production.")
    return value


def comma_separated(value):
    return [part.strip() for part in value.split(",") if part.strip()]


APP_ENV = os.environ.get("APP_ENV", "production")
if APP_ENV not in {"development", "production"}:
    raise ImproperlyConfigured("APP_ENV must be development or production.")

DEBUG = APP_ENV == "development"
if DEBUG:
    SECRET_KEY = "django-insecure-local-development-only"
    ALLOWED_HOSTS = ["localhost", "127.0.0.1", "[::1]"]
    DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.sqlite3",
            "NAME": os.environ.get("APP_SQLITE_PATH") or BASE_DIR / "db.sqlite3",
        },
    }
else:
    SECRET_KEY = require_env("APP_SECRET_KEY")
    if (
        len(SECRET_KEY) < 50
        or len(set(SECRET_KEY)) < 5
        or SECRET_KEY.startswith("django-insecure-")
    ):
        raise ImproperlyConfigured("APP_SECRET_KEY must be a securely generated key.")

    ALLOWED_HOSTS = comma_separated(require_env("APP_ALLOWED_HOSTS"))
    if not ALLOWED_HOSTS or any("*" in host or host.startswith(".") for host in ALLOWED_HOSTS):
        raise ImproperlyConfigured("APP_ALLOWED_HOSTS must list explicit hosts.")

    postgres_options = {"sslmode": "verify-full"}
    if ca_file := os.environ.get("APP_DB_SSLROOTCERT"):
        postgres_options["sslrootcert"] = ca_file

    DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.postgresql",
            "NAME": require_env("APP_DB_NAME"),
            "USER": require_env("APP_DB_USER"),
            "PASSWORD": require_env("APP_DB_PASSWORD"),
            "HOST": require_env("APP_DB_HOST"),
            "PORT": os.environ.get("APP_DB_PORT", "5432"),
            "OPTIONS": postgres_options,
        },
    }

    SECURE_SSL_REDIRECT = True
    SECURE_HSTS_SECONDS = 31536000
    SESSION_COOKIE_SECURE = True
    CSRF_COOKIE_SECURE = True

    # Enable only behind a trusted proxy that overwrites this header.
    if os.environ.get("APP_TRUST_PROXY_HTTPS") == "1":
        SECURE_PROXY_SSL_HEADER = ("HTTP_X_FORWARDED_PROTO", "https")


INSTALLED_APPS = [
    "django.contrib.auth",
    "django.contrib.contenttypes",
    "django.contrib.sessions",
    "django.contrib.messages",
    "django.contrib.staticfiles",
    "governance",
]

MIDDLEWARE = [
    "django.middleware.security.SecurityMiddleware",
    "django.contrib.sessions.middleware.SessionMiddleware",
    "django.middleware.common.CommonMiddleware",
    "django.middleware.csrf.CsrfViewMiddleware",
    "django.contrib.auth.middleware.AuthenticationMiddleware",
    "django.contrib.messages.middleware.MessageMiddleware",
    "django.middleware.clickjacking.XFrameOptionsMiddleware",
]

ROOT_URLCONF = "config.urls"
WSGI_APPLICATION = "config.wsgi.application"

TEMPLATES = [
    {
        "BACKEND": "django.template.backends.django.DjangoTemplates",
        "DIRS": [BASE_DIR / "templates"],
        "APP_DIRS": True,
        "OPTIONS": {
            "context_processors": [
                "django.template.context_processors.request",
                "django.contrib.auth.context_processors.auth",
                "django.contrib.messages.context_processors.messages",
            ],
        },
    },
]

AUTH_PASSWORD_VALIDATORS = [
    {"NAME": "django.contrib.auth.password_validation.UserAttributeSimilarityValidator"},
    {"NAME": "django.contrib.auth.password_validation.MinimumLengthValidator"},
    {"NAME": "django.contrib.auth.password_validation.CommonPasswordValidator"},
    {"NAME": "django.contrib.auth.password_validation.NumericPasswordValidator"},
]

LOGIN_URL = "login"
LOGIN_REDIRECT_URL = "home"
LOGOUT_REDIRECT_URL = "login"

LANGUAGE_CODE = "zh-hans"
TIME_ZONE = "Asia/Shanghai"
USE_I18N = True
USE_TZ = True

STATIC_URL = "/static/"
STATIC_ROOT = BASE_DIR / "staticfiles"
if (BASE_DIR / "static").is_dir():
    STATICFILES_DIRS = [BASE_DIR / "static"]

DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"
