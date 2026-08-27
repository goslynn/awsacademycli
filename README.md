# awsacademycli

Controla el [AWS Academy Learner Lab](https://awsacademy.instructure.com) desde la terminal.

Hace por vos el recorrido de siempre —entrar a Canvas, abrir el laboratorio, pulsar
*Start Lab*, esperar, abrir *AWS Details* y copiar las tres claves a `~/.aws/credentials`—
y lo reduce a un comando. Como las credenciales del laboratorio duran unas pocas horas,
ese recorrido se repite varias veces al día.

```console
$ awsacademy start
laboratorio: AWS Academy Learner Lab
arrancando…
  estado: arrancando
  estado: corriendo

Laboratorio listo.
  queda        3h58m0s de sesión
  perfil       academy -> credential_process
  ✓ arn:aws:sts::123456789012:assumed-role/voclabs/user1234567
```

## Instalación

Necesita Go 1.24 o superior. No necesita navegador ni ninguna otra dependencia.

```console
$ go install github.com/goslynn/awsacademycli/cmd/awsacademy@latest
```

Desde el repositorio:

```console
$ CGO_ENABLED=0 go build -o awsacademy ./cmd/awsacademy
```

## Uso

```console
$ awsacademy setup     # una vez: guarda tus credenciales y localiza el laboratorio
$ awsacademy start     # levanta el laboratorio y actualiza el perfil de AWS
$ awsacademy courses   # lista tus cursos y fija cuál tiene el laboratorio
$ awsacademy status    # ¿puedo trabajar? ¿cuánto tiempo me queda?
$ awsacademy stop      # baja el laboratorio
```

### Evitar `--profile` en cada comando

```console
$ awsacademy default-profile
```

Apunta el perfil `default` de `~/.aws/config` al mismo proveedor. A partir de
ahí, `aws sts get-caller-identity` funciona sin más, igual que
`--profile academy`.

Se resuelve en la configuración de AWS, no en el shell, así que funciona igual
en cualquier distribución, con cualquier shell, y también en macOS y Windows.
No usa variables de entorno ni toca tus ficheros de arranque.

> Nunca pisa un perfil por defecto ajeno en silencio: si ya hay claves
> estáticas, otro `credential_process`, una sesión SSO o un rol asumido, avisa
> de qué encontró y pide confirmación. `--undo` lo deshace, y solo retira lo que
> puso: los ajustes que hubieras añadido a mano se quedan.

Para algo puntual en una sola sesión existe además `eval "$(awsacademy env)"`,
que exporta `AWS_PROFILE`.

## Cómo entrega las credenciales

Por defecto configura `credential_process` en `~/.aws/config`:

```ini
[profile academy]
credential_process = /home/vos/go/bin/awsacademy creds
region = us-east-1
```

Así el AWS CLI le pide las credenciales a esta herramienta cuando las necesita, las
cachea y las renueva sola, sin que queden credenciales vencidas escritas en disco.

> **Ojo con la precedencia.** Dentro de un mismo perfil, las claves estáticas de
> `~/.aws/credentials` tienen prioridad sobre el `credential_process` de
> `~/.aws/config`. Si dejás las dos cosas, el proveedor queda de adorno y seguirás
> usando credenciales muertas. `setup` detecta esta colisión y se ofrece a limpiarla.

Si preferís el modo clásico, `awsacademy start --write-credentials` escribe el perfil
en `~/.aws/credentials` preservando el resto de tus perfiles.

## Ficheros

| Ruta | Contenido |
|---|---|
| `~/.config/awsacademy/config.toml` | Credenciales de AWS Academy y curso elegido. Permisos **0600**, obligatorios |
| `~/.local/state/awsacademy/session.json` | Cookies de la sesión |
| `~/.local/state/awsacademy/discovery.json` | Qué curso, qué ítem y qué endpoints son el laboratorio |
| `~/.local/state/awsacademy/creds.json` | Últimas credenciales, caché de `credential_process` |

Todo lo de `state/` es caché reconstruible: se puede borrar sin perder nada.

En vez de guardar la contraseña en claro podés delegar en un gestor externo:

```toml
password_command = "pass show aws/academy"
```

## Cómo funciona

No usa navegador. El recorrido completo, tal como se verificó contra el servicio real:

1. **Login en Canvas.** Sirve su login con React, pero por debajo sigue siendo Rails
   clásico: un POST a `/login/canvas` con el token CSRF que viene en una cookie. Sin
   captcha. Se pide `remember_me`, así que la cookie persistente dura semanas y la
   contraseña casi nunca hace falta.
2. **Descubrimiento por API.** Canvas expone su API REST a la sesión, así que el curso
   y el ítem del laboratorio se resuelven en JSON en vez de scrapeando menús. Un curso
   típico tiene siete ítems de herramienta externa y casi todos mencionan el
   "Laboratorio de aprendizaje" —la guía, las demostraciones, las preguntas frecuentes—
   así que se eligen por proveedor LTI, no por título.
3. **Lanzamiento LTI 1.3.** El formulario firmado no está en el iframe: Canvas lo deja
   en `about:blank` y lo rellena por JavaScript. El formulario de verdad está oculto en
   la propia página, marcado con `data-message-type="tool_launch"`. A partir de ahí el
   baile OIDC —`oidc_login.php`, `authorize_redirect`, `authorize`, y el `id_token`
   devuelto en un formulario auto-enviado— es solo seguir redirects y reenviar
   formularios. El mismo mecanismo cubre LTI 1.1, así que no hace falta saber de
   antemano qué versión usa el curso.
4. **Trampolines de Vocareum.** El proveedor no sirve el panel directamente: devuelve
   dos páginas cuyo único contenido es un script que navega a la siguiente
   (`launch.php` → `main.php?m=editor` → `main.php?m=clabide`). No hace falta un motor
   de JavaScript para seguirlas, solo leer la URL.
5. **La API del laboratorio.** Vocareum atiende todo por `util/vcput.php` y distingue la
   operación con `a=`: `startaws`, `endaws`, `getawsstatus`, `getaws`. Esas URLs llevan
   un `stepid` propio de la sesión, así que **no se pueden compilar como constantes**:
   se leen de la página del laboratorio, que es donde sus propios botones las declaran.

**Nada de esto está hardcodeado.** El curso cambia cada término, con él cambian todas
las URLs, y los identificadores de sesión cambian en cada lanzamiento. Todo se
re-descubre cuando hace falta.

## Desarrollo

```console
$ go test ./...          # nada de esto toca la red
$ gofmt -l . && go vet ./...
```

Los tests levantan un Canvas y un Vocareum simulados y ejercitan la cadena entera,
incluido el auto-submit del formulario firmado.

### Diagnosticar el descubrimiento

Si el laboratorio deja de responder, lo primero es ver qué expone la página:

```console
$ awsacademy debug lab --scripts
```

Atraviesa el lanzamiento LTI e imprime todas las rutas de endpoint que encuentra, más
las que la herramienta está usando. No necesita navegador.

Cuando eso no alcance, se puede capturar el tráfico real:

```console
$ go run ./cmd/vockit -out captura.json
```

Abre un navegador, hacés el flujo a mano una vez y quedan volcados todos los XHR con su
URL, método, cuerpo y respuesta. Los valores confirmados van a `discovery.json`, que
tiene prioridad sobre las conjeturas compiladas: corregirlos no exige recompilar.

> La captura contiene cookies de sesión y credenciales en claro. No la publiques.

## Aviso

Automatiza el acceso a tu propia cuenta con tus propias credenciales, haciendo el mismo
recorrido que haría tu navegador. Aun así se comporta con moderación deliberada: un
request en vuelo a la vez, sin paralelismo, backoff ante 429 y 5xx, y un User-Agent que
se identifica en vez de disfrazarse de navegador.
