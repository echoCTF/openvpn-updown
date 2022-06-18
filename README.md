# echoCTF OpenVPN UpDown
A small tool written in Go that is used by OpenVPN `client-connect` and `client-disconnect`.

The tool is reponsible for providing the following functionality
* Checks if the event is active before proceeding
* Calls the `VPN_LOGIN()` / `VPN_LOGOUT()` procedures on the database
* Retrieves existing networks that the user is granted access
* Retrieves private instance networks that the user is granted access
* Executes `pfctl` to add the user VPN assigned IP to their client tables (`<networkcodename_clients>`)
