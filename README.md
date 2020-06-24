# History
Initial commits look to have come from [Marius Magureanu](github.com/mariusmagureanu) Credit where it's due.
I found the code with [Guillaume Quintard](https://github.com/gquintard) at [Github.com/gquintard/broadcaster](https://github.com/gquintard/broadcaster)
If you want to buy anyone a drink as thanks for this, please send it to Marius or Guillaume
At the time of writing this code looked like it had been abandoned with historic and non-working links in the installation details and within code.
I imagine that this sort of functionality is what seperates the free and commercial [Varnish](<https://www.varnish-cache.org/>) varnish offerings, however
if this code is incorporated into the [Varnish github account](https://github.com/varnish) I'll re-work my changes to make appropriate pull requests and once
merged, discontinue this fork.


# Request distributor
Broadcasts requests to multiple [Varnish](<https://www.varnish-cache.org/>) caches from a single entry point.
The initial thought is to ease-up purging/banning across multiple [Varnish](<https://www.varnish-cache.org/>) cache instances.
The broadcaster consists out of a web-server which will handle the distribution of requests against all configured caches.

## Installation

    go get -u github.com/timothyclarke/http-request-broadcaster

## Usage

See [this](caches.ini) file as an example on how to configure your caches.

Start the app with any of the following command line args:

  - **port**: The port under which the broadcaster is exposed. Defaults to **8088**.
  - **goroutines**: Sets the number of available goroutines which will handle the broadcast against the caches. Defaults to a number of **8**, a higher number does not necesarilly imply a better performance. Can be tweaked though depending on the number of caches.
  - **cfg**: Path to an .ini file containing configured caches. This is a *required* parameter.
  - **retries**: Number of items to retry if a request fails to execute. Defaults to 1.
  - **enforce**: If true, the response code will be set according to the first non-200 received from the Varnish nodes.
  - **log-file**: Path to a log file. If none specified it defaults to ```stdout```.
  - **enable-log**: Switches logging on/off. Disabled by default.

#### HTTPS support.

  By default, the broadcaster starts listening on the http port, however - if both ``crt`` and ``key`` options are set, it will automatically switch onto https.

  - **https-port**: Broadcaster https listening port. If none specified it defaults to **8443**.
  - **crt**: CRT file used for HTTPS support.
  - **key**: KEY file used for HTTPS support.

#### Optional headers.

   - **X-Group**: Name of the group to broadcast against, if not used - the broadcast will be done against all caches.

#### Configuration reload.

   If the broadcaster receives a ``SIGHUP`` notification, it will trigger a configuration reload from disk.

## Examples:

Purge **/something/to/purge** in all caches within the ``[default]`` group:
```
curl -is http://localhost:8088/something/to/purge -H "X-Group: default"  -X PURGE
```

Ban **/foo** in all caches within the ``[prod]`` group:
```
curl -is http://localhost:8088/foo -H "X-Group: prod"  -X BAN
```

Purge everything in all your caches:
```
curl -is http://localhost:8088/ -X PURGE
```

Purge everything in all your caches over https:
```
curl -is https://localhost:8443/ -X PURGE --cacert server.crt
```

Output sample:
```
HTTP/1.1 200 OK
Content-Type: application/json
Date: Tue, 06 Dec 2016 11:03:07 GMT
Content-Length: 164

{
  "Cache11": 200,
  "Cache12": 200,
  "Cache13": 501
}
```

Note that your VCL needs to be aware of your purging/banning intentions. See [here](https://www.varnish-cache.org/docs/trunk/users-guide/purging.html) for more cache invalidation details.
